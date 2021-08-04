package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	timeout, _ := time.ParseDuration("5s")

	for i := 0; i < 10; i++ {
		startAt := time.Now()
		conn, err := net.DialTimeout("tcp", "www.chicagotribune.com:443", timeout)
		endAt := time.Now()
		if err != nil {
			fmt.Printf("Dial failed: %v\n", err)
		}
		remoteAddr := conn.RemoteAddr()
		conn.Close()

		fmt.Printf("TCP Ping to %v: %v\n", remoteAddr.String(), endAt.Sub(startAt))
		time.Sleep(1 * time.Second)
	}
}
