package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/go-ping/ping"
)

type OpLat struct {
	// Destination to ping
	DstIP string

	// Protocol type of probes (e.g. TCP, ICMP)
	Protocol string

	// Host of iPerf3 server to use for speedtest
	Host string

	// Port of iPerf3 server to use for speedtest
	Port string

	// Number of Probes to send
	NumProbes int

	// Timeout specifies a timeout before ping exits, regardless of how many
	// packets have been received. Default is 6s
	Timeout time.Duration

	// Interval is the wait time between each packet send. Default is 0.5s
	Interval time.Duration

	// Length of Iperf3 test
	Length time.Duration

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

	// Test upload latency under load
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

func testICMP(test *OpLat) {

	lat := pingICMP(test)

	c := make(chan []byte)
	go speedtest(c, test.Host, test.Port, test.Length, test.Download)

	time.Sleep(5 * time.Second)

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

	fmtResults(test)
}

func pingICMP(test *OpLat) *ping.Statistics {
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

func testTCP(test *OpLat) {

	var UnloadedRtts []time.Duration
	var LoadedRtts []time.Duration
	c := make(chan time.Duration)
	iperf := make(chan []byte)

	// Test unloaded latency
	go pingTCP(test.DstIP, test.Timeout, test.Interval, test.NumProbes, c)

	for {
		res := <-c
		UnloadedRtts = append(UnloadedRtts, res)
		if len(UnloadedRtts) == test.NumProbes {
			break
		}
	}

	// Test loaded latency
	go speedtest(iperf, test.Host, test.Port, test.Length, test.Download)

	// Wait several seconds before pinging to not catch iperf during slow start
	time.Sleep(5 * time.Second)

	ping_start := time.Now()
	var ping_end time.Duration
	var iperf_end time.Duration
	var out []byte

	go pingTCP(test.DstIP, test.Timeout, test.Interval, test.NumProbes, c)
	for i := 0; i <= test.NumProbes; i++ {
		select {
		case res := <-c:
			LoadedRtts = append(LoadedRtts, res)
			if len(LoadedRtts) == test.NumProbes {
				ping_end = time.Since(ping_start)
			}
		case out = <-iperf:
			iperf_end = time.Since(ping_start)
		}
	}

	if iperf_end < ping_end {
		fmt.Println(errors.New(fmt.Sprintf("iPerf3 finished before pings, suggested test length increase: %v", ping_end-iperf_end)))
	}

	test.UnloadedRtt = UnloadedRtts
	test.LoadedRtt = LoadedRtts
	UpdateTCPStatistics(test.UnloadedRtt, &test.UnloadedStats, test.NumProbes)
	UpdateTCPStatistics(test.LoadedRtt, &test.LoadedStats, test.NumProbes)
	parseRes(test, out, iperf_end-ping_end)

	fmtResults(test)

}

func pingTCP(dst string, timeout time.Duration,
	interval time.Duration, n int, c chan time.Duration) {

	ticker := time.NewTicker(interval)

	for i := 0; i < n; i++ {
		<-ticker.C
		go func(dst string, timeout time.Duration) {
			startAt := time.Now()
			conn, err := net.DialTimeout("tcp", dst+":443", timeout)
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

	fmt.Printf("res: %v\n", res)

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

	x, _ := time.ParseDuration(fmt.Sprintf("%vs", PacketsRecv))
	stats.AvgRtt = AvgRtt / x
	stats.MaxRtt = MaxRtt
	stats.MinRtt = MinRtt
	stats.PacketsRecv = PacketsRecv
	stats.PacketsSent = PacketsSent
	stats.PacketLoss = float64(PacketsRecv) / float64(PacketsSent)
}

func main() {
	dstip := flag.String("d", "8.8.8.8", "Destination to ping")
	proto := flag.String("m", "icmp", "Method (protocol) of probing")
	n := flag.Int("n", 5, "Number of packets")
	ptimeout := flag.String("timeout", "6", "Ping timeout (in seconds)")
	pinterval := flag.String("i", "0.5", "inter-ping time (in seconds)")
	testLength := flag.String("t", "10", "Iperf3 test length")
	verb := flag.Bool("v", true, "Verbose output")
	down := flag.Bool("R", false, "Reverse mode (test download)")
	host := flag.String("c", "", "Iperf3 server hostname")
	port := flag.String("p", "", "Iperf3 server port")

	flag.Parse()

	if *host == "" || *port == "" {
		fmt.Println("Please specify Iperf3 host and port...Exiting")
	}

	timeout, _ := time.ParseDuration(fmt.Sprintf("%vs", *ptimeout))
	interval, _ := time.ParseDuration(fmt.Sprintf("%vs", *pinterval))
	length, _ := time.ParseDuration(fmt.Sprintf("%vs", *testLength))

	test := OpLat{
		DstIP:     *dstip,
		Protocol:  *proto,
		Host:      *host,
		Port:      *port,
		NumProbes: *n,
		Timeout:   timeout,
		Interval:  interval,
		Length:    length,
		Verbose:   *verb,
		Download:  *down,
		UnloadedStats: Statistics{
			PacketsSent: *n,
		},
		LoadedStats: Statistics{
			PacketsSent: *n,
		},
	}

	if test.Protocol == "icmp" {
		testICMP(&test)
	} else if test.Protocol == "tcp" {
		testTCP(&test)
	} else {
		fmt.Println(errors.New(fmt.Sprintf("Unrecognized protocol: %v. Choose from TCP or ICMP", test.Protocol)))
	}
}

func speedtest(c chan []byte, host string, port string,
	length time.Duration, download bool) {

	reverse := ""
	if download {
		reverse = "-R"
	}

	out, err := exec.Command("iperf3", "-c", host, "-p", port,
		"-J", reverse, "-t", length.String()).Output()
	if err != nil {
		log.Fatal(err)
	}

	c <- out
}

func parseRes(test *OpLat, res []byte, diff time.Duration) {
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

func fmtResults(test *OpLat) {

	direction := "Upstream"
	if test.Download {
		direction = "Downstream"
	}

	// Unloaded latency statistics
	fmt.Printf("\n--- %s Unloaded (%s) Latency Statistics to %s---\n", direction, test.Protocol, test.DstIP)
	fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
		test.UnloadedStats.PacketsSent, test.UnloadedStats.PacketsRecv, test.UnloadedStats.PacketLoss)
	fmt.Printf("round-trip min/avg/max = %v/%v/%v\n",
		test.UnloadedStats.MinRtt, test.UnloadedStats.AvgRtt, test.UnloadedStats.MaxRtt)

	// Loaded latency statistics
	fmt.Printf("\n--- %s Loaded (%s) Latency Statistics to %s---\n", direction, test.Protocol, test.DstIP)
	fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
		test.LoadedStats.PacketsSent, test.LoadedStats.PacketsRecv, test.LoadedStats.PacketLoss)
	fmt.Printf("round-trip min/avg/max = %v/%v/%v/\n",
		test.LoadedStats.MinRtt, test.LoadedStats.AvgRtt, test.LoadedStats.MaxRtt)

	// Verbose output
	if test.Verbose {
		fmt.Println("\n--- Iperf3 Status at Probe Time---")
		writer := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', tabwriter.AlignRight)

		if test.Download {
			fmt.Fprintln(writer, "[Probe RTT]\t[Load (Mb/s)]")
			for _, item := range test.PingLoads {
				fmt.Fprintf(writer, "%v\t%v\n", item.Rtt, item.Rate)
			}
		} else {
			fmt.Fprintln(writer, "[Probe RTT]\t[Load (Mb/s)]\t[Retransmissions]")
			for _, item := range test.PingLoads {
				fmt.Fprintf(writer, "%v\t%v\t%v\n", item.Rtt, item.Rate, item.Retransmits)
			}
		}

		writer.Flush()
	}
}
