package server

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestScanFileWithClamAVProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reply    string
		infected bool
	}{
		{name: "clean", reply: "stream: OK\x00"},
		{name: "infected", reply: "stream: Eicar-Test-Signature FOUND\x00", infected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			previousDial := dialClamAV
			dialClamAV = func(string, string, time.Duration) (net.Conn, error) {
				return client, nil
			}
			defer func() { dialClamAV = previousDial }()
			received := make(chan []byte, 1)
			go func() {
				connection := server
				defer connection.Close()
				var acceptErr error
				command := make([]byte, len("zINSTREAM\x00"))
				if _, acceptErr = io.ReadFull(connection, command); acceptErr != nil {
					return
				}
				var body []byte
				for {
					var size [4]byte
					if _, acceptErr = io.ReadFull(connection, size[:]); acceptErr != nil {
						return
					}
					length := binary.BigEndian.Uint32(size[:])
					if length == 0 {
						break
					}
					chunk := make([]byte, length)
					if _, acceptErr = io.ReadFull(connection, chunk); acceptErr != nil {
						return
					}
					body = append(body, chunk...)
				}
				received <- body
				_, _ = connection.Write([]byte(tc.reply))
			}()

			file, err := os.CreateTemp("", "clamav-test-*")
			if err != nil {
				t.Fatal(err)
			}
			path := file.Name()
			defer os.Remove(path)
			if _, err = file.WriteString("payload"); err != nil {
				t.Fatal(err)
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}

			infected, err := scanFileWithClamAV(path, "clamav:3310", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if infected != tc.infected {
				t.Fatalf("infected: want %v, got %v", tc.infected, infected)
			}
			select {
			case body := <-received:
				if string(body) != "payload" {
					t.Fatalf("stream body = %q", body)
				}
			case <-time.After(time.Second):
				t.Fatal("clamav test server received no stream")
			}
		})
	}
}
