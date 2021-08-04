package main

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func localPort(dstip net.IP) (net.IP, int) {
	serverAddr, err := net.ResolveUDPAddr("udp", dstip.String()+":12345")
	if err != nil {
		log.Fatal(err)
	}

	if conn, err := net.DialUDP("udp", nil, serverAddr); err == nil {
		if udpaddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return udpaddr.IP, udpaddr.Port
		}
	}
	log.Fatal("Count not get local ip: " + err.Error())
	return nil, -1
}

func main() {

	dstaddrs, err := net.LookupIP("www.chicagotribune.com")
	if err != nil {
		log.Fatal(err)
	}

	for _, ip := range dstaddrs {
		fmt.Println(ip.String())
	}

	dstip := dstaddrs[0].To4()
	var dstport layers.TCPPort
	if d, err := strconv.ParseUint("443", 10, 16); err != nil {
		log.Fatal(err)
	} else {
		dstport = layers.TCPPort(d)
	}

	srcip, sport := localPort(dstip)

	fmt.Printf("Src IP: %v\n", srcip)
	fmt.Printf("Src port: %v\n", sport)
	fmt.Printf("Dst port: %v\n", dstport)

	srcport := layers.TCPPort(sport)

	ip := &layers.IPv4{
		SrcIP:    srcip,
		DstIP:    dstip,
		Protocol: layers.IPProtocolTCP,
	}

	tcp := &layers.TCP{
		SrcPort: srcport,
		DstPort: dstport,
		Seq:     rand.Uint32(),
		SYN:     true,
		Window:  14600,
	}

	tcp.SetNetworkLayerForChecksum(ip)
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{
		ComputeChecksums: true,
		FixLengths:       true,
	}

	if err := gopacket.SerializeLayers(buf, opts, tcp); err != nil {
		log.Fatal(err)
	}

	conn, err := net.ListenPacket("ip4:tcp", "0.0.0.0")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	log.Println("writing request")

	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Fatal(err)
	}

	t := time.Now()
	if _, err := conn.WriteTo(buf.Bytes(), &net.IPAddr{IP: dstip}); err != nil {
		log.Fatal(err)
	}

	for {
		b := make([]byte, 4096)
		log.Println("reading from conn")
		n, addr, err := conn.ReadFrom(b)
		fmt.Printf("Time taken: %v\n", time.Now().Sub(t))
		if err != nil {
			log.Println("error reading packet: ", err)
			return
		} else if addr.String() == dstip.String() {
			packet := gopacket.NewPacket(b[:n], layers.LayerTypeTCP, gopacket.Default)

			if tcpLayer := packet.Layer(layers.LayerTypeTCP); tcpLayer != nil {
				tcp, _ := tcpLayer.(*layers.TCP)

				if tcp.DstPort == srcport {
					if tcp.SYN && tcp.ACK {
						log.Printf("Port %d is OPEN\n", dstport)
					} else {
						log.Printf("Port %d is CLOSED\n", dstport)
					}
					return
				}
			}
		} else {
			log.Printf("Got packet not matching addr")
		}
	}
}
