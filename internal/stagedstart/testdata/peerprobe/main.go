// peerprobe is a trusted synthetic adversary for the keeper's socket boundary.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(1)
	}
	if os.Args[1] == "deny" {
		c, err := net.Dial("unix", "@crewship-staged-v1")
		if err != nil {
			os.Exit(2)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(time.Second))
		if json.NewEncoder(c).Encode(map[string]string{"operation": "status"}) != nil {
			return
		}
		var response struct{ Error string }
		err = json.NewDecoder(c).Decode(&response)
		if err == nil && response.Error == "" {
			os.Exit(3)
		}
		return
	}
	if os.Args[1] != "flood" {
		os.Exit(1)
	}
	var peers []net.Conn
	for i := 0; i < 32; i++ {
		c, err := net.DialTimeout("unix", "@crewship-staged-v1", time.Second)
		if err != nil {
			os.Exit(2)
		}
		peers = append(peers, c)
	}
	// Let Accept drain the backlog before announcing the resource attack.
	time.Sleep(250 * time.Millisecond)
	fmt.Println("READY")
	time.Sleep(4 * time.Second)
	for _, c := range peers {
		_ = c.Close()
	}
}
