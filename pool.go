package keypool

import "errors"

var (
	//ErrClosed 连接池已经关闭Error
	ErrClosed = errors.New("pool is closed")
)

// Pool 基本方法
type Pool[T any] interface {
	Get(string) (*IdleConn[T], error)

	Put(string, *IdleConn[T]) error

	Close(string, *IdleConn[T]) error

	Release()

	Len(string) int
}
