package keypool

import (
	"math/rand"
	"net"
	"net/http"
	"net/rpc"
	"sync"
	"testing"
	"time"
)

var (
	MaxIdleCap = 10
	MaximumCap = 100
	network    = "tcp"
	address    = "127.0.0.1:7777"
	//factory    = func() (interface{}, error) { return net.Dial(network, address) }
	factory = func(key string) (*rpc.Client, error) {
		return rpc.DialHTTP("tcp", address)
	}
	closeFac = func(nc *rpc.Client) error {
		return nc.Close()
	}
)

func init() {
	// used for factory function
	go rpcServer()
	time.Sleep(time.Millisecond * 300) // wait until tcp server has been settled

	rand.Seed(time.Now().UTC().UnixNano())
}

func TestNew(t *testing.T) {
	p, err := newChannelPool()
	defer p.Release()
	if err != nil {
		t.Errorf("New error: %s", err)
	}
}
func TestPool_Get_Impl(t *testing.T) {
	p, _ := newChannelPool()
	defer p.Release()
	key := "test"

	conn, err := p.Get(key)
	if err != nil {
		t.Errorf("Get error: %s", err)
	}

	p.Put(key, conn)
}

func BenchmarkPoolGetPut(b *testing.B) {
	p, _ := newChannelPool()
	defer p.Release()
	key := "test"
	var (
		conn [10]*idleConn[*rpc.Client]
	)
	for n := 0; n < b.N; n++ {
		for i := 0; i < 10; i++ {
			c, err := p.Get(key)
			if err != nil {
				b.Errorf("Get error: %s", err)
			}
			conn[i] = c
		}

		for i := 0; i < 10; i++ {
			p.Put(key, conn[i])
		}
	}

	//	goos: linux
	//
	// goarch: amd64
	// pkg: github.com/holdno/keypool
	// cpu: Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz
	// BenchmarkPoolGetPut
	// BenchmarkPoolGetPut-2   	 3598538	       309.6 ns/op	      52 B/op	       1 allocs/op

// 	goos: linux
// goarch: amd64
// pkg: github.com/holdno/keypool
// cpu: Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz
// BenchmarkPoolGetPut
// BenchmarkPoolGetPut-2
//   190255	     13700 ns/op	     566 B/op	      10 allocs/op
// PASS
// ok  	github.com/holdno/keypool	3.013s

// BenchmarkPoolGetPut-2   	     433	   2522522 ns/op	   38360 B/op	      12 allocs/op
}

func TestPool_Get(t *testing.T) {
	p, _ := newChannelPool()
	defer p.Release()
	key := "test"
	_, err := p.Get(key)
	if err != nil {
		t.Errorf("Get error: %s", err)
	}

	// after one get, current capacity should be lowered by one.
	// if p.Len(key) != (InitialCap - 1) {
	// 	t.Errorf("Get error. Expecting %d, got %d",
	// 		(InitialCap - 1), p.Len(key))
	// }

	// get them all
	var wg sync.WaitGroup
	for i := 0; i < (MaximumCap - 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Get(key)
			if err != nil {
				t.Errorf("Get error: %s", err)
			}
		}()
	}
	wg.Wait()

	if p.Len(key) != 0 {
		t.Errorf("Get error. Expecting 0, got %d", p.Len(key))
	}

	_, err = p.Get(key)
	if err != ErrMaxActiveConnReached {
		t.Errorf("Get error: %s", err)
	}

}

func TestPool_Put(t *testing.T) {
	key := "test"
	pconf := Config[*rpc.Client]{MaxCap: MaximumCap, Factory: factory, Close: closeFac, IdleTimeout: time.Second * 20,
		MaxIdle: MaxIdleCap}
	p, err := NewChannelPool(&pconf)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Release()

	// get/create from the pool
	conns := make([]*idleConn[*rpc.Client], MaximumCap)
	for i := 0; i < MaximumCap; i++ {
		conn, _ := p.Get(key)
		conns[i] = conn
	}

	// now put them all back
	for _, conn := range conns {
		p.Put(key, conn)
	}

	if p.Len(key) != MaxIdleCap {
		t.Errorf("Put error len. Expecting %d, got %d",
			1, p.Len(key))
	}

	p.Release() // close pool

}

// func TestPool_UsedCapacity(t *testing.T) {
// 	p, _ := newChannelPool()
// 	key := "test"
// 	defer p.Release()

// 	if p.Len(key) != InitialCap {
// 		t.Errorf("InitialCap error. Expecting %d, got %d",
// 			InitialCap, p.Len(key))
// 	}
// }

func TestPool_Close(t *testing.T) {
	p, _ := newChannelPool()
	key := "test"
	// now close it and test all cases we are expecting.
	p.Release()

	c := p.(*channelPool[*rpc.Client])

	if c.conns[key] != nil {
		t.Errorf("Close error, conns channel should be nil")
	}

	if c.factory != nil {
		t.Errorf("Close error, factory should be nil")
	}

	_, err := p.Get(key)
	if err == nil {
		t.Errorf("Close error, get conn should return an error")
	}

	if p.Len(key) != 0 {
		t.Errorf("Close error used capacity. Expecting 0, got %d", p.Len(key))
	}
}

func TestPoolConcurrent(t *testing.T) {
	p, _ := newChannelPool()
	pipe := make(chan *idleConn[*rpc.Client], 0)

	go func() {
		p.Release()
	}()

	key := "test"
	for i := 0; i < MaximumCap; i++ {
		go func() {
			conn, _ := p.Get(key)

			pipe <- conn
		}()

		go func() {
			conn := <-pipe
			if conn == nil {
				return
			}
			p.Put(key, conn)
		}()
	}

	time.Sleep(time.Second * 3)
}

func TestPoolWriteRead(t *testing.T) {
	//p, _ := NewChannelPool(0, 30, factory)
	key := "test"
	p, _ := newChannelPool()
	conn, _ := p.Get(key)
	cli := conn.Conn()
	var resp int
	err := cli.Call("Arith.Multiply", Args{1, 2}, &resp)
	if err != nil {
		t.Error(err)
	}
	if resp != 2 {
		t.Error("rpc.err")
	}
}

func TestPoolConcurrent2(t *testing.T) {
	//p, _ := NewChannelPool(0, 30, factory)
	p, _ := newChannelPool()

	var wg sync.WaitGroup
	key := "test"
	go func() {
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				conn, _ := p.Get(key)
				time.Sleep(time.Millisecond * time.Duration(rand.Intn(100)))
				p.Close(key, conn)
				wg.Done()
			}(i)
		}
	}()

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			conn, _ := p.Get(key)
			time.Sleep(time.Millisecond * time.Duration(rand.Intn(100)))
			p.Close(key, conn)
			wg.Done()
		}(i)
	}

	wg.Wait()
}

//
//func TestPoolConcurrent3(t *testing.T) {
//	p, _ := NewChannelPool(0, 1, factory)
//
//	var wg sync.WaitGroup
//
//	wg.Add(1)
//	go func() {
//		p.Close()
//		wg.Done()
//	}()
//
//	if conn, err := p.Get(); err == nil {
//		conn.Close()
//	}
//
//	wg.Wait()
//}

func newChannelPool() (Pool[*rpc.Client], error) {
	pconf := Config[*rpc.Client]{MaxCap: MaximumCap, Factory: factory, Close: closeFac, IdleTimeout: time.Second * 20,
		MaxIdle: MaxIdleCap}
	return NewChannelPool(&pconf)
}

func rpcServer() {
	arith := new(Arith)
	rpc.Register(arith)
	rpc.HandleHTTP()

	l, e := net.Listen("tcp", address)
	if e != nil {
		panic(e)
	}
	go http.Serve(l, nil)
}

type Args struct {
	A, B int
}

type Arith int

func (t *Arith) Multiply(args *Args, reply *int) error {
	*reply = args.A * args.B
	return nil
}
