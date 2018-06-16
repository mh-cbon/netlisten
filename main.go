package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
)

func main() {
	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		log.Fatal("invalid command line, expected: netlisten [addr] to [dst]")
	}

	srcAddr := args[0]
	dstAddr := "-"
	if len(args) > 2 {
		dstAddr = args[2]
	}

	var dst io.Writer
	{
		x, err := write(dstAddr)
		if err != nil {
			log.Fatalf("failed to open %q, err=%v", dstAddr, err)
		}
		if y, ok := x.(io.Closer); ok {
			defer y.Close()
		}
		dst = x
	}

	var listener net.Listener
	{
		x, err := listen(srcAddr)
		if err != nil {
			log.Fatalf("failed to listen %q, err=%v", srcAddr, err)
		}
		defer x.Close()
		listener = x
	}

	var wg sync.WaitGroup
	{
		accept(&wg, listener, func(s net.Conn) {
			sname := fmt.Sprintf("%v copy from %v to %v", listener.Addr(), s.RemoteAddr(), dstAddr)
			copy(dst, s, sname)
		})
	}

	waitOrCancel(&wg)
}

func waitOrCancel(wg *sync.WaitGroup) {
	sig := make(chan os.Signal, 10)
	signal.Notify(sig)

	wait := make(chan struct{})
	go func() {
		wg.Wait()
		wait <- struct{}{}
	}()

	select {
	case <-sig:
	case <-wait:
	}
}

func accept(wg *sync.WaitGroup, l net.Listener, handle func(net.Conn)) {
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("accept err=%v\n", err)
			break
		}
		log.Printf("%v accepted %v\n", l.Addr(), conn.RemoteAddr())
		wg.Add(1)
		go func() {
			handle(conn)
			wg.Done()
		}()
	}
}

func copy(dst io.Writer, src io.ReadCloser, name string) {
	defer src.Close()
	n, err := io.Copy(dst, src)
	log.Printf("%q copied %v bytes\n", name, n)
	if err != nil {
		log.Fatalf("%q failed to copy, err=%v", name, err)
	}
}

var prefixes = map[string]string{
	"tcp://":  "tcp",
	"tcp6://": "tcp6",
	"udp://":  "udp",
	"udp6://": "udp6",
}

func listen(some string) (l net.Listener, err error) {

	found := false
	for prefix, network := range prefixes {
		if strings.HasPrefix(some, prefix) {
			l, err = net.Listen(network, some[len(prefix):])
			found = true
		}
	}
	if !found {
		if p := strings.Split(some, ":"); len(p) == 2 {
			l, err = net.Listen("tcp", some)

		} else if f, e := os.Open(some); e == nil {
			l, err = net.FileListener(f)
			l = fileListenerCloser{
				Listener: l,
				closer:   f,
			}
		} else {
			return nil, e
		}

	}
	return
}

type fileListenerCloser struct {
	net.Listener
	closer io.Closer
}

func (l fileListenerCloser) Close() error { l.closer.Close(); return l.Listener.Close() }

func write(some string) (dst io.Writer, err error) {
	if some == "-" {
		dst = &noCloser{os.Stdout}
	} else {
		found := false
		for prefix, network := range prefixes {
			if strings.HasPrefix(some, prefix) {
				dst, err = net.Dial(network, some[len(prefix):])
				found = true
			}
		}
		if !found {
			if p := strings.Split(some, ":"); len(p) == 2 {
				dst, err = net.Dial("tcp", some)
			} else {
				dst, err = os.Open(some)
			}
		}
	}
	return
}

type noCloser struct {
	io.Writer
}

func (n *noCloser) Close() error { return nil }
