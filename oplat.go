package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"text/tabwriter"
	"time"

	"github.com/go-ping/ping"
)

type OpLat struct {
	// Destination to ping
	DstIP string

	// Number of probes to send
	NumProbes int

	// Timeout specifies a timeout before ping exits, regardless of how many
	// packets have been received. Default is 6s
	Timeout time.Duration

	// Interval is the wait time between each packet send. Default is 0.5s
	Interval time.Duration

	// Length of Iperf3 test
	Length time.Duration

	// statistics of probe rtts without iperf load
	Stats *ping.Statistics

	// statistics of probe rtts with iperf load
	LoadStats *ping.Statistics

	// Pairing probes with Iperf interval stats
	PingLoads []LatRate

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

func main() {
	dstip := flag.String("d", "8.8.8.8", "Destination to ping")
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
		NumProbes: *n,
		Timeout:   timeout,
		Interval:  interval,
		Length:    length,
		Verbose:   *verb,
		Download:  *down,
	}

	lat := pingICMP(&test)

	c := make(chan []byte)
	go speedtest(c, *host, *port, test.Length, test.Download)

	time.Sleep(5 * time.Second)

	ping_start := time.Now()
	oplat := pingICMP(&test)

	ping_end := time.Since(ping_start)
	res := <-c

	diff := time.Since(ping_start) - ping_end

	test.Stats = lat
	test.LoadStats = oplat
	parseRes(&test, res, diff)

	fmtResults(&test)

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

func pingICMP(test *OpLat) *ping.Statistics {
	pinger, err := ping.NewPinger(test.DstIP)
	if err != nil {
		panic(err)
	}

	// Configure pinging parameters
	pinger.Count = test.NumProbes
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
		(test.LoadStats.Rtts[len(test.LoadStats.Rtts)-1])

	for i, rtt := range test.LoadStats.Rtts {
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
	fmt.Printf("\n--- %s Unloaded Latency Statistics to %s---\n", direction, test.DstIP)
	fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
		test.Stats.PacketsSent, test.Stats.PacketsRecv, test.Stats.PacketLoss)
	fmt.Printf("round-trip min/avg/max/stddev = %v/%v/%v/%v\n",
		test.Stats.MinRtt, test.Stats.AvgRtt, test.Stats.MaxRtt, test.Stats.StdDevRtt)

	// Loaded latency statistics
	fmt.Printf("\n--- %s Loaded Latency Statistics to %s---\n", direction, test.DstIP)
	fmt.Printf("%d packets transmitted, %d packets received, %v%% packet loss\n",
		test.LoadStats.PacketsSent, test.LoadStats.PacketsRecv, test.LoadStats.PacketLoss)
	fmt.Printf("round-trip min/avg/max/stddev = %v/%v/%v/%v\n",
		test.LoadStats.MinRtt, test.LoadStats.AvgRtt,
		test.LoadStats.MaxRtt, test.LoadStats.StdDevRtt)

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
