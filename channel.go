package keypool

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	//"reflect"
)

var (
	//ErrMaxActiveConnReached 连接池超限
	ErrMaxActiveConnReached = errors.New("MaxActiveConnReached")
)

// Config 连接池相关配置
type Config[T any] struct {
	//最大并发存活连接数
	MaxCap int
	//最大空闲连接
	MaxIdle int
	//生成连接的方法
	Factory func(key string) (T, error)
	//关闭连接的方法
	Close func(T) error
	//检查连接是否有效的方法
	Ping func(T) error
	//连接最大空闲时间，超过该事件则将失效
	IdleTimeout time.Duration
}

type connReq[T any] struct {
	idleConn *idleConn[T]
}

// channelPool 存放连接信息
type channelPool[T any] struct {
	mu                       sync.RWMutex
	conns                    map[string]chan *idleConn[T]
	factory                  func(key string) (T, error)
	close                    func(T) error
	ping                     func(T) error
	idleTimeout, waitTimeOut time.Duration
	maxActive                int
	maxIdle                  int
	openingConns             int
	connReqs                 map[string][]chan connReq[T]
	openerCh                 chan string

	ttl *ttl[T]
}

type ttl[T any] struct {
	close       func(T) error
	idleTimeout time.Duration
	index       map[[16]byte]*idleConn[T]
	mu          sync.RWMutex
}

func (t *ttl[T]) add(conn *idleConn[T]) {
	t.mu.Lock()
	t.index[conn.id] = conn
	t.mu.Unlock()
}

func (t *ttl[T]) loop() {
	if t.idleTimeout == 0 {
		return
	}
	timeTicker := time.NewTicker(time.Nanosecond * 10)
	var now time.Time
	for {
		select {
		case <-timeTicker.C:
			now = time.Now()
			t.mu.Lock()
			for k, v := range t.index {
				if now.Sub(v.lt) > t.idleTimeout {
					if v.mu.TryLock() {
						t.close(v.Conn())
						delete(t.index, k)
						v.mu.Unlock()
					}
				}
			}
			t.mu.Unlock()
		}
	}
}

func (c *channelPool[T]) genNewConn(key string) (*idleConn[T], error) {
	conn, err := c.factory(key)
	if err != nil {
		return nil, err
	}

	ic := &idleConn[T]{
		id:   ulid.Make(),
		conn: conn,
		t:    time.Now(),
		lt:   time.Now(),
	}

	return ic, nil
}

type idleConn[T any] struct {
	id   [16]byte
	conn T
	t    time.Time
	lt   time.Time
	mu   sync.Mutex
}

// const (
// 	CONN_STATUS_IDLE int32 = iota
// 	CONN_STATUS_BUSY
// )

func (i *idleConn[T]) Conn() T {
	return i.conn
}

var connectionRequestQueueSize = 1000000

// NewChannelPool 初始化连接
func NewChannelPool[T any](poolConfig *Config[T]) (Pool[T], error) {
	if poolConfig.MaxCap < poolConfig.MaxIdle {
		return nil, errors.New("invalid capacity settings")
	}
	if poolConfig.Factory == nil {
		return nil, errors.New("invalid factory func settings")
	}
	if poolConfig.Close == nil {
		return nil, errors.New("invalid close func settings")
	}

	c := &channelPool[T]{
		conns:        make(map[string]chan *idleConn[T]),
		factory:      poolConfig.Factory,
		close:        poolConfig.Close,
		idleTimeout:  poolConfig.IdleTimeout,
		maxActive:    poolConfig.MaxCap,
		maxIdle:      poolConfig.MaxIdle,
		openingConns: 0,
		openerCh:     make(chan string, connectionRequestQueueSize),
		ttl: &ttl[T]{
			idleTimeout: poolConfig.IdleTimeout,
			index:       make(map[[16]byte]*idleConn[T]),
			close:       poolConfig.Close,
		},
	}

	if poolConfig.Ping != nil {
		c.ping = poolConfig.Ping
	}

	go c.connectionOpener()
	go c.ttl.loop()

	return c, nil
}

// getConns 获取所有连接
func (c *channelPool[T]) getConns(key string) chan *idleConn[T] {
	c.mu.Lock()
	if _, exist := c.conns[key]; !exist {
		c.conns[key] = make(chan *idleConn[T], c.maxIdle)
	}
	if _, exist := c.connReqs[key]; !exist {
		c.connReqs = make(map[string][]chan connReq[T])
	}
	conns := c.conns[key]
	c.mu.Unlock()
	return conns
}

// connectionOpener separate goroutine for opening new connection
func (c *channelPool[T]) connectionOpener() {
	for {
		select {
		case key, ok := <-c.openerCh:
			if !ok {
				return
			}
			c.openNewConnection(key)
		}
	}
}

// openNewConnection Open one new connection
func (c *channelPool[T]) openNewConnection(key string) {
	conn, err := c.genNewConn(key)
	if err != nil {
		c.mu.Lock()
		c.openingConns--
		c.maybeOpenNewConnections(key)
		c.mu.Unlock()

		// put nil connection into pool to wake up pending channel fetch
		c.Put(key, nil)
		return
	}

	c.Put(key, conn)
}

// Get 从pool中取一个连接
func (c *channelPool[T]) Get(key string) (*idleConn[T], error) {
	conns := c.getConns(key)
	if conns == nil {
		return nil, ErrClosed
	}
	for {
		select {
		case wrapConn := <-conns:
			if wrapConn == nil {
				return nil, ErrClosed
			}

			if !wrapConn.mu.TryLock() {
				continue
			}
			//判断是否失效，失效则丢弃，如果用户没有设定 ping 方法，就不检查
			if c.ping != nil {
				if err := c.Ping(wrapConn.conn); err != nil {
					c.Close(key, wrapConn)
					continue
				}
			}
			return wrapConn, nil
		default:
			c.mu.Lock()
			if c.openingConns >= c.maxActive {
				req := make(chan connReq[T], 1)
				c.connReqs[key] = append(c.connReqs[key], req)
				c.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()
				var ret connReq[T]
				select {
				case <-ctx.Done():
					close(req)
					return nil, ErrMaxActiveConnReached
				case ret = <-req:
				}
				if ret.idleConn == nil {
					return nil, errors.New("failed to create a new connection")
				}
				if c.idleTimeout > 0 {
					if ret.idleConn.lt.Add(c.idleTimeout).Before(time.Now()) {
						//丢弃并关闭该连接
						c.Close(key, ret.idleConn)
						continue
					}
				}
				return ret.idleConn, nil
			}
			if c.factory == nil {
				c.mu.Unlock()
				return nil, ErrClosed
			}

			// c.factory 耗时较长，采用乐观策略，先增加，失败后再减少
			c.openingConns++
			c.mu.Unlock()
			conn, err := c.genNewConn(key)
			if err != nil {
				c.mu.Lock()
				c.openingConns--
				c.mu.Unlock()
				return nil, err
			}
			if !conn.mu.TryLock() {
				continue
			}
			return conn, nil
		}
	}
}

// Put 将连接放回pool中
func (c *channelPool[T]) Put(key string, conn *idleConn[T]) error {
	c.mu.Lock()
	if c.conns == nil && conn != nil {
		c.mu.Unlock()
		return c.Close(key, conn)
	}

	conn.lt = time.Now()
	conn.mu.Unlock()
	c.ttl.add(conn)

	if l := len(c.connReqs[key]); l > 0 {
		req := c.connReqs[key][0]
		copy(c.connReqs[key], c.connReqs[key][1:])
		c.connReqs[key] = c.connReqs[key][:l-1]
		if conn == nil {
			req <- connReq[T]{idleConn: nil}
			//return errors.New("connection is nil. rejecting")
		} else {
			req <- connReq[T]{
				idleConn: conn,
			}
		}
		c.mu.Unlock()
		return nil
	} else if conn != nil {
		select {
		case c.conns[key] <- conn:
			c.mu.Unlock()
			return nil
		default:
			c.mu.Unlock()
			//连接池已满，直接关闭该连接
			return c.Close(key, conn)
		}
	}

	c.mu.Unlock()
	return errors.New("connection is nil, rejecting")
}

// maybeOpenNewConnections 如果有请求在，并且池里的连接上限未达到时，开启新的连接
// Assumes c.mu is locked
func (c *channelPool[T]) maybeOpenNewConnections(key string) {
	numRequest := len(c.connReqs)

	if c.maxActive > 0 {
		numCanOpen := c.maxActive - c.openingConns
		if numRequest > numCanOpen {
			numRequest = numCanOpen
		}
	}
	for numRequest > 0 {
		c.openingConns++
		numRequest--
		c.openerCh <- key
	}
}

// Close 关闭单条连接
func (c *channelPool[T]) Close(key string, conn *idleConn[T]) error {
	if conn == nil {
		return errors.New("connection is nil. rejecting")
	}
	if c.close == nil {
		return nil
	}

	var err error
	err = c.close(conn.Conn())

	c.mu.Lock()
	c.openingConns--
	c.maybeOpenNewConnections(key)
	c.mu.Unlock()
	return err
}

// Ping 检查单条连接是否有效
func (c *channelPool[T]) Ping(conn T) error {
	return c.ping(conn)
}

// Release 释放连接池中所有连接
func (c *channelPool[T]) Release() {
	c.mu.Lock()
	conns := c.conns
	c.conns = make(map[string]chan *idleConn[T])
	c.factory = nil
	c.ping = nil
	closeFun := c.close
	c.close = nil
	openerCh := c.openerCh
	c.openerCh = nil
	c.mu.Unlock()

	if conns == nil {
		return
	}

	// close channels
	for _, v := range conns {
		close(v)
	}

	if openerCh != nil {
		close(openerCh)
	}

	for _, connsChan := range conns {
		//log.Printf("Type %v\n",reflect.TypeOf(wrapConn.conn))
		for conn := range connsChan {
			closeFun(conn.Conn())
		}
	}
}

// Len 连接池中已有的连接
func (c *channelPool[T]) Len(key string) int {
	return len(c.getConns(key))
}
