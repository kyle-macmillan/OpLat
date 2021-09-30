package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/go-ping/ping"
)

type OpLat struct {
	// Absolute path to iPerf3 executable
	Path string

	// Host of iPerf3 server to use for speedtest
	Host string

	//Port of iPerf3 server to use for speedtest
	Port string

	// ICMP Pinger
	ICMPinger Pinger

	// TCP Pinger
	TCPinger Pinger

	// Test upload latency under load
	Download bool

	// Length of Iperf3 test
	Length time.Duration

	// Flag if tcp test is included
	tcp bool

	// Flag if icmp test is included
	icmp bool

	// Sum of data received during iPerf3 test (bytes)
	SumRecv float64

	// Sum of data sent during iPerf3 test (bytes)
	SumSent float64
}

type Pinger struct {
	// Desintation IP to ping
	DstIP string

	// Destination port to ping
	TCPPort string
	// Protocol type of probes (e.g. TCP, ICMP)
	Protocol string

	// Number of Probes to send
	NumProbes int

	// Length of iPerf3 Test (used to calculate ping intervals
	Length time.Duration

	// Timeout specifies a timeout before ping exits, regardless of how many
	// packets have been received. Default is 6s
	Timeout time.Duration

	// Interval is the wait time between each packet send. Default is 0.5s
	Interval time.Duration

	// Rtts without iperf load
	UnloadedRtt []time.Duration

	// Rtts with iperf load
	LoadedRtt []time.Duration

	// Pairing probes with Iperf interval stats
	PingLoads []LatRate

	// Rtt stats without iperf load
	UnloadedStats Statistics

	// Rtt stats with iperf load
	LoadedStats Statistics

	// Setting to true prints extra info about probes
	Verbose bool

	// Test download latency under load
	Download bool
}

type LatRate struct {
	// Rtt of probe
	Rtt time.Duration

	// Rate of Iperf3 at time of probe (Mb/s)
	Rate float64

	// Numer of retransmits in this interval
	Retransmits float64
}

type Statistics struct {
	// Number of packets received
	PacketsRecv int

	// Number of packets sent
	PacketsSent int

	// Percent of packets lost
	PacketLoss float64

	// Minimum round-trip time of probes sent
	MinRtt time.Duration

	// Maximum round-trip time of probes sent
	MaxRtt time.Duration

	// Average round-trip time of probes sent
	AvgRtt time.Duration
}

func Control(test *OpLat, wg *sync.WaitGroup) {

	cTCP := make(chan []byte)
	cICMP := make(chan []byte)
	cIPERF := make(chan []byte)

	if test.tcp {
		wg.Add(1)
		go testTCP(&test.TCPinger, cTCP, wg)
	}
	if test.icmp {
		wg.Add(1)
		go testICMP(&test.ICMPinger, cICMP, wg)
	}

	// Wait to begin speedtest until finished unloaded
	if test.tcp {
		<-cTCP
	}
	if test.icmp {
		<-cICMP
	}

	go speedtest(cIPERF, test)
	time.Sleep(4 * time.Second)

	if test.tcp {
		cTCP <- []byte{}
	}

	if test.icmp {
		cICMP <- []byte{}
	}

	res := <-cIPERF

	if test.tcp {
		cTCP <- res
	}

	if test.icmp {
		cICMP <- res
	}

	getUsage(test, res)

}

func getUsage(test *OpLat, res []byte) {

	var f map[string]interface{}

	json.Unmarshal(res, &f)

	test.SumSent = f["end"].(map[string]interface{})["sum_sent"].(map[string]interface{})["bytes"].(float64)
	test.SumRecv = f["end"].(map[string]interface{})["sum_received"].(map[string]interface{})["bytes"].(float64)
}

func testICMP(test *Pinger, c chan []byte, wg *sync.WaitGroup) {

	defer wg.Done()

	lat := pingICMP(test)

	// Signal to control that unloaded ping test is complete
	c <- []byte{}

	// Block until iPerf3 has been running for 4 seconds
	<-c

	ping_start := time.Now()
	oplat := pingICMP(test)

	ping_end := time.Since(ping_start)
	res := <-c

	diff := time.Since(ping_start) - ping_end

	test.UnloadedRtt = lat.Rtts
	test.LoadedRtt = oplat.Rtts

	updateICMPStatistics(lat, &test.UnloadedStats)
	updateICMPStatistics(oplat, &test.LoadedStats)
	parseRes(test, res, diff)
}

func pingICMP(test *Pinger) *ping.Statistics {
	pinger, err := ping.NewPinger(test.DstIP)
	if err != nil {
		panic(err)
	}

	// Configure pinging parameters
	pinger.Count = test.UnloadedStats.PacketsSent
	pinger.Timeout = test.Timeout
	pinger.Interval = test.Interval

	// Run pings
	err = pinger.Run()
	if err != nil {
		panic(err)
	}

	// Gather statistics
	return pinger.Statistics()
}

func updateICMPStatistics(res *ping.Statistics, stats *Statistics) {
	stats.AvgRtt = res.AvgRtt
	stats.MaxRtt = res.MaxRtt
	stats.MinRtt = res.MinRtt
	stats.PacketLoss = res.PacketLoss
	stats.PacketsRecv = res.PacketsRecv
	stats.PacketsSent = res.PacketsSent
}

func testTCP(test *Pinger, c chan []byte, wg *sync.WaitGroup) {

	defer wg.Done()

	var UnloadedRtts []time.Duration
	var LoadedRtts []time.Duration
	tcpChan := make(chan time.Duration)

	// Test unloaded latency
	go pingTCP(test.DstIP, test.TCPPort, test.Timeout,
		test.Interval, test.NumProbes, tcpChan)

	for {
		res := <-tcpChan
		UnloadedRtts = append(UnloadedRtts, res)
		if len(UnloadedRtts) == test.NumProbes {
			break
		}
	}

	// Signal to control that unloaded ping test is complete
	c <- []byte{}

	// Block until iPerf3 has run for 4 seconds
	<-c
	ping_start := time.Now()
	var ping_end time.Duration
	var iperf_end time.Duration
	var out []byte

	go pingTCP(test.DstIP, test.TCPPort, test.Timeout,
		test.Interval, test.NumProbes, tcpChan)
	for i := 0; i <= test.NumProbes; i++ {
		select {
		case res := <-tcpChan:
			LoadedRtts = append(LoadedRtts, res)
			if len(LoadedRtts) == test.NumProbes {
				ping_end = time.Since(ping_start)
			}
		case out = <-c:
			iperf_end = time.Since(ping_start)
		}
	}

	if iperf_end < ping_end {
		log.Fatal(errors.New(fmt.Sprintf("iPerf3 finished before pings, suggested test length increase: %v", ping_end-iperf_end)))
	}

	test.UnloadedRtt = UnloadedRtts
	test.LoadedRtt = LoadedRtts
	UpdateTCPStatistics(test.UnloadedRtt, &test.UnloadedStats, test.NumProbes)
	UpdateTCPStatistics(test.LoadedRtt, &test.LoadedStats, test.NumProbes)
	parseRes(test, out, iperf_end-ping_end)
}

func pingTCP(dst string, port string, timeout time.Duration,
	interval time.Duration, n int, c chan time.Duration) {

	ticker := time.NewTicker(interval)

	for i := 0; i < n; i++ {
		<-ticker.C
		go func(dst string, timeout time.Duration) {
			startAt := time.Now()
			conn, err := net.DialTimeout("tcp", dst+":"+port, timeout)
			endAt := time.Now()

			// Return -1 if any errors during connection
			if err != nil {
				c <- -1 * time.Second
				return
			} else {
				c <- endAt.Sub(startAt)
			}
			conn.Close()
		}(dst, timeout)

	}
	ticker.Stop()
}

func UpdateTCPStatistics(res []time.Duration, stats *Statistics,
	PacketsSent int) {
	MinRtt := 100 * time.Second
	MaxRtt := 0 * time.Second
	AvgRtt := 0 * time.Second
	PacketsRecv := 0

	for _, rtt := range res {
		if rtt == -1*time.Second {
			continue
		}
		PacketsRecv++
		AvgRtt += rtt

		if rtt < MinRtt {
			MinRtt = rtt
		}

		if rtt > MaxRtt {
			MaxRtt = rtt
		}
	}

	if PacketsRecv == 0 {
		stats.AvgRtt = -1
		stats.MaxRtt = -1
		stats.MinRtt = -1
	} else {
		stats.AvgRtt = AvgRtt / time.Duration(PacketsRecv)
		stats.MaxRtt = MaxRtt
		stats.MinRtt = MinRtt
	}
	stats.PacketsRecv = PacketsRecv
	stats.PacketsSent = PacketsSent
	stats.PacketLoss = 100 - (float64(PacketsRecv)/float64(PacketsSent))*100.0
}

func speedtest(c chan []byte, test *OpLat) {

	reverse := ""
	if test.Download {
		reverse = "-R"
	}

	timeout := test.Length + time.Second*10

	ctx := context.Background()
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, test.Path, "-c", test.Host, "-p", test.Port,
		"-J", reverse, "-t", test.Length.String())

	// If iperf3 doesn't terminated within test length + 10secs, kill process
	if out, err := cmd.Output(); err != nil {
		fmt.Println("Error launching or completing iPerf3")
		log.Fatal(err)
		cmd.Process.Kill()
	} else {
		c <- out
	}
}

func parseRes(test *Pinger, res []byte, diff time.Duration) {
	var rates []float64
	var retransmits []float64
	var f map[string]interface{}

	json.Unmarshal(res, &f)
	for _, item := range f["intervals"].([]interface{}) {
		m := item.(map[string]interface{})
		for _, interval := range m["streams"].([]interface{}) {
			m = interval.(map[string]interface{})
			rates = append(rates, m["bits_per_second"].(float64)*1e-6)
			if !test.Download {
				retransmits = append(retransmits, m["retransmits"].(float64))
			}
		}
	}
	lastInterval := test.Length - diff -
		(test.LoadedRtt[len(test.LoadedRtt)-1])

	for i, rtt := range test.LoadedRtt {
		rate := rates[int(math.Floor(lastInterval.Seconds()-test.Interval.Seconds()*float64(i)))]

		if test.Download {
			test.PingLoads = append(test.PingLoads, LatRate{Rtt: rtt, Rate: rate})
		} else {
			retransmit := retransmits[int(math.Floor(lastInterval.Seconds()-test.Interval.Seconds()*float64(i)))]
			test.PingLoads = append(test.PingLoads, LatRate{Rtt: rtt, Rate: rate, Retransmits: retransmit})
		}
	}
}

func Output(test *OpLat, FmtJSON bool) {

	if FmtJSON {
		dat, _ := json.Marshal(test)
		fmt.Println(string(dat))
		return
	}

	if test.icmp {
		OutputPlain(&test.ICMPinger)
	}

	if test.tcp {
		OutputPlain(&test.TCPinger)
	}
}

func OutputPlain(test *Pinger) {

	direction := "Upstream"
	if test.Download {
		direction = "Downstream"
	}

	// Unloaded latency statistics
	fmt.Printf("\n--- %s Unloaded (%s) Latency Statistics to %s---\n", direction, test.Protocol, test.DstIP)
	if test.UnloadedStats.AvgRtt == -1 {
		fmt.Println("NO RESPONSE DURING UNLOADED TEST")
	} else {
		fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
			test.UnloadedStats.PacketsSent, test.UnloadedStats.PacketsRecv, test.UnloadedStats.PacketLoss)
		fmt.Printf("round-trip min/avg/max = %v/%v/%v\n",
			test.UnloadedStats.MinRtt, test.UnloadedStats.AvgRtt, test.UnloadedStats.MaxRtt)
	}

	// Loaded latency statistics
	fmt.Printf("\n--- %s Loaded (%s) Latency Statistics to %s---\n", direction, test.Protocol, test.DstIP)
	if test.LoadedStats.AvgRtt == -1 {
		fmt.Println("NO RESPONSE DURING LOADED TEST")
		return
	} else {
		fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
			test.LoadedStats.PacketsSent, test.LoadedStats.PacketsRecv, test.LoadedStats.PacketLoss)
		fmt.Printf("round-trip min/avg/max = %v/%v/%v/\n",
			test.LoadedStats.MinRtt, test.LoadedStats.AvgRtt, test.LoadedStats.MaxRtt)
	}

	// Verbose output
	if test.Verbose {
		fmt.Println("\n--- Iperf3 Status at Probe Time---")
		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', tabwriter.AlignRight)

		if test.Download {
			fmt.Fprintln(writer, "[Probe RTT]\t[Load (Mb/s)]")
			for i := len(test.PingLoads) - 1; i >= 0; i-- {
				fmt.Fprintf(writer, "%v\t%v\n", test.PingLoads[i].Rtt,
					test.PingLoads[i].Rate)
			}
		} else {
			fmt.Fprintln(writer, "[Probe RTT]\t[Load (Mb/s)]\t[Retransmissions]")
			for i := len(test.PingLoads) - 1; i >= 0; i-- {
				fmt.Fprintf(writer, "%v\t%v\t%v\n", test.PingLoads[i].Rtt,
					test.PingLoads[i].Rate, test.PingLoads[i].Retransmits)
			}
		}
		writer.Flush()
	}
}

func main() {
	iperfPath := flag.String("s", "", "Path to iperf3 executable")
	dst := flag.String("d", "8.8.8.8", "Destination to ping")
	proto := flag.String("m", "icmp", "Method (protocol) of probing")
	n := flag.Int("n", 5, "Number of packets")
	ptimeout := flag.String("timeout", "6", "Ping timeout (in seconds)")
	pinterval := flag.String("i", "0.5", "inter-ping time (in seconds)")
	testLength := flag.String("t", "13", "Iperf3 test length")
	verb := flag.Bool("v", true, "Verbose output")
	down := flag.Bool("R", false, "Reverse mode (test download)")
	host := flag.String("c", "", "Iperf3 server hostname")
	port := flag.String("p", "", "Iperf3 server port")
	FmtJSON := flag.Bool("J", false, "Print output as json")

	flag.Parse()

	if *host == "" || *port == "" {
		fmt.Println("Please specify Iperf3 host and port...Exiting")
	}

	timeout, _ := time.ParseDuration(fmt.Sprintf("%vs", *ptimeout))
	interval, _ := time.ParseDuration(fmt.Sprintf("%vs", *pinterval))
	length, _ := time.ParseDuration(fmt.Sprintf("%vs", *testLength))

	test := OpLat{
		Path:     *iperfPath,
		Host:     *host,
		Port:     *port,
		Length:   length,
		Download: *down,
		tcp:      false,
		icmp:     false,
	}

	for _, ele := range strings.Split(*proto, " ") {
		if ele == "icmp" {
			test.icmp = true
			test.ICMPinger = Pinger{
				DstIP:     strings.Split(*dst, ":")[0],
				Protocol:  "icmp",
				NumProbes: *n,
				Length:    length,
				Timeout:   timeout,
				Interval:  interval,
				Download:  *down,
				Verbose:   *verb,
				UnloadedStats: Statistics{
					PacketsSent: *n,
				},
				LoadedStats: Statistics{
					PacketsSent: *n,
				},
			}
		}
		if ele == "tcp" {
			if len(strings.Split(*dst, ":")) != 2 {
				fmt.Println(errors.New(fmt.Sprintf(
					"TCP Dst Addr in wrong format. Found '%v', need [ip]:[port]", *dst)))
				return
			}
			test.tcp = true
			test.TCPinger = Pinger{
				DstIP:     strings.Split(*dst, ":")[0],
				TCPPort:   strings.Split(*dst, ":")[1],
				Protocol:  "tcp",
				NumProbes: *n,
				Length:    length,
				Timeout:   timeout,
				Interval:  interval,
				Download:  *down,
				Verbose:   *verb,
				UnloadedStats: Statistics{
					PacketsSent: *n,
				},
				LoadedStats: Statistics{
					PacketsSent: *n,
				},
			}
		}
	}

	if !test.icmp && !test.tcp {
		fmt.Println(errors.New(fmt.Sprintf("Unrecognized protocols: %v. Choose from TCP or ICMP", *proto)))
		return
	}

	var wg sync.WaitGroup
	Control(&test, &wg)

	wg.Wait()
	Output(&test, *FmtJSON)
}
