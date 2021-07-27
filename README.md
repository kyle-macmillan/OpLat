# OpLat
This is a utility to test end-to-end latency under high network utilization. 
Iperf3 is used to generate the load and go-ping to measure the latency. 

## Requirements
Must have Iperf3 in path. The utility currently calls Iperf3 as a subprocess. 

## Basic Usage
Test Downstream Latency:
`./oplat -c=[Iperf3 hostname] -p=[Iperf3 port] -d=[Ping Destination] -R`
Testing Upstream Latency:
`./oplat -c=[Iperf3 hostname] -p=[Iperf3 port] -d=[Ping Destination]`


## Help
Use the command `go run oplat.go -h` or `./oplat -h` to view configurable 
parameters.
