package main

import (
	"fmt"
	"net"
	"time"
)

func main() {
	timeout, _ := time.ParseDuration("5s")

	for i := 0; i < 1; i++ {
		startAt := time.Now()
		conn, err := net.DialTimeout("tcp", "24.1.155.73:443", timeout)
		endAt := time.Now()
		remoteAddr := conn.RemoteAddr()
		fmt.Printf("TCP Ping to %v: %v\n", remoteAddr.String(), endAt.Sub(startAt))
		if err != nil {
			fmt.Printf("Dial failed: %v\n", err)
		}

		time.Sleep(5 * time.Second)
		conn.Close()
		time.Sleep(1 * time.Second)
	}
}
