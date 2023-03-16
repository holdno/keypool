# keypool

Golang 实现的连接池，基于 github.com/silenceper/pool 改造实现

## 功能：

- 连接池中连接类型为`泛型 T`，使得更加通用
- 连接的最大空闲时间，超时的连接将关闭丢弃，可避免空闲时连接自动失效问题
- 支持用户设定 ping 方法，检查连接的连通性，无效的连接将丢弃
- 使用 channel 处理池中的连接，高效

## 基本用法

```go
// 创建方法
factory = func(key string) (*rpc.Client, error) {
		return rpc.DialHTTP("tcp", address)
	}

// 关闭方法
closeFac = func(nc *rpc.Client) error {
		return nc.Close()
	}

//ping 检测连接的方法
//ping := func(v interface{}) error { return nil }

//创建一个连接池： 初始化5，最大空闲连接是20，最大并发连接30
poolConfig := &pool.Config{
	MaxIdle:    20,//最大空闲连接数
	MaxCap:     30,//最大并发连接数
	Factory:    factory,
	Close:      close,
	//Ping:       ping,
	//连接最大空闲时间，超过该时间的连接 将会关闭，可避免空闲时连接EOF，自动失效的问题
	IdleTimeout: 15 * time.Second,
}
p, err := pool.NewChannelPool(poolConfig)
if err != nil {
	fmt.Println("err=", err)
}

//从连接池中取得一个连接
v, err := p.Get("test")

//do something
//conn=v.(net.Conn)

//将连接放回连接池中
p.Put("test",v)

//释放连接池中的所有连接
p.Release()

//查看当前连接中的数量
current := p.Len()


```

#### 注:

该连接池参考 [https://github.com/fatih/pool](https://github.com/fatih/pool) 实现，改变以及增加原有的一些功能。

## License

The MIT License (MIT) - see LICENSE for more details
