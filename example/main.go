package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	pool "github.com/holdno/keypool"
)

const addr string = "127.0.0.1:8080"

func main() {
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt, os.Kill, syscall.SIGUSR1, syscall.SIGUSR2)
	go server()
	//等待tcp server启动
	time.Sleep(2 * time.Second)
	client()
	fmt.Println("使用: ctrl+c 退出服务")
	<-c
	fmt.Println("服务退出")
}

func client() {

	//factory 创建连接的方法
	factory := func(key string) (net.Conn, error) { return net.Dial("tcp", addr) }

	//close 关闭连接的方法
	close := func(v net.Conn) error { return v.Close() }
	key := "example"
	//创建一个连接池：最大连接5，空闲连接数是4
	poolConfig := &pool.Config[net.Conn]{
		MaxIdle: 4,
		MaxCap:  5,
		Factory: factory,
		Close:   close,
		//连接最大空闲时间，超过该时间的连接 将会关闭，可避免空闲时连接EOF，自动失效的问题
		IdleTimeout: 15 * time.Second,
	}
	p, err := pool.NewChannelPool(poolConfig)
	if err != nil {
		fmt.Println("err=", err)
	}

	//从连接池中取得一个连接
	v, err := p.Get(key)

	//do something
	//conn=v.(net.Conn)

	//将连接放回连接池中
	p.Put(key, v)

	//释放连接池中的所有连接
	//p.Release()

	//查看当前连接中的数量
	current := p.Len(key)
	fmt.Println("len=", current)
}

func server() {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("Error listening: ", err)
		os.Exit(1)
	}
	defer l.Close()
	fmt.Println("Listening on ", addr)
	for {
		conn, err := l.Accept()
		if err != nil {
			fmt.Println("Error accepting: ", err)
		}
		fmt.Printf("Received message %s -> %s \n", conn.RemoteAddr(), conn.LocalAddr())
		//go handleRequest(conn)
	}
}
