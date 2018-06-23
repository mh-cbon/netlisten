package main

import (
	"context"
	_ "expvar"
	"flag"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/oxtoacart/bpool"
)

var (
	log = logAPI{}
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var opts = struct {
		timeout   time.Duration
		monitor   string
		maxAccept int
	}{}

	flag.DurationVar(&opts.timeout, "t", time.Second*60, "timeout of the conenction")
	flag.IntVar(&opts.maxAccept, "k", 0, "kill after that many connections")
	flag.StringVar(&opts.monitor, "monitor", ":", "http monitor address")

	flag.Parse()

	go func() {
		if err := http.ListenAndServe(opts.monitor, nil); err != nil {
			log.Fatal("monitoring server could not listen, err=%v", err)
		}
	}()

	args := flag.Args()
	delta := 1.2

	if len(args) < 1 {
		log.Fatal("invalid command line, expected: netlisten [addr] to [dst] [cap]")
	}

	srcAddr := args[0]
	dstAddr := "-"
	if len(args) > 1 {
		dstAddr = args[1]
	}

	capBytes := uint64(float64(1024*20) * delta)
	if len(args) > 2 {
		b, err := bytefmt.ToBytes(args[2])
		if err != nil {
			log.Fatal("failed to parse capacity %q, err=%v", args[3], err)
		}
		capBytes = uint64(float64(b) * delta)
	}

	var dst io.Writer
	{
		x, err := write(dstAddr)
		if err != nil {
			log.Fatal("failed to open %q, err=%v", dstAddr, err)
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
			log.Fatal("failed to listen %q, err=%v", srcAddr, err)
		}
		defer x.Close()
		listener = x
	}

	var wg sync.WaitGroup
	{
		bufpool := bpool.NewBytePool(48, int(float64(capBytes)*delta))
		accept(ctx, &wg, opts.maxAccept, listener, func(src net.Conn) {
			src = &idleTimeoutConn{Conn: src, timeout: opts.timeout}
			start := time.Now()
			buf := bufpool.Get()
			defer bufpool.Put(buf)
			n, err := io.CopyBuffer(dst, src, buf)
			if err != nil {
				log.Print("[ERR] %v -> %v %v",
					src.RemoteAddr(), dstAddr, err,
				)
				return
			}
			elapsed := time.Since(start)
			copied := bytefmt.ByteSize(uint64(n))
			speed := bytefmt.ByteSize(uint64(n / int64(elapsed.Seconds())))
			log.Print("%v -> %v copied %v - %v - %v/s",
				src.RemoteAddr(), dstAddr,
				copied, elapsed, speed,
			)
		})
	}

	waitOrCancel(ctx, &wg)
}

func waitOrCancel(ctx context.Context, wg *sync.WaitGroup) {
	sig := make(chan os.Signal, 10)
	signal.Notify(sig, os.Interrupt, os.Kill)

	wait := make(chan struct{})
	go func() {
		wg.Wait()
		wait <- struct{}{}
	}()

	select {
	case <-ctx.Done():
	case <-sig:
	case <-wait:
	}
}

func accept(ctx context.Context, wg *sync.WaitGroup, max int, l net.Listener, handle func(net.Conn)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		var accepted int
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if max > 0 && accepted >= max {
				return
			}
			conn, err := l.Accept()
			if err != nil {
				log.Print("accept err=%v\n", err)
				continue
			}
			if max > 0 && accepted >= max {
				return
			}
			accepted++
			log.Print("%v accepted %v\n", l.Addr(), conn.RemoteAddr())
			wg.Add(1)
			go func() {
				defer conn.Close()
				handle(conn)
				wg.Done()
			}()
		}
	}()
}

func copy(dst io.Writer, src io.ReadCloser, name string) {
	defer src.Close()
	n, err := io.Copy(dst, src)
	log.Print("%q copied %v bytes\n", name, n)
	if err != nil {
		log.Error("%q failed to copy, err=%v", name, err)
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

type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (i idleTimeoutConn) Read(buf []byte) (n int, err error) {
	i.Conn.SetDeadline(time.Now().Add(i.timeout))
	n, err = i.Conn.Read(buf)
	// i.Conn.SetDeadline(time.Now().Add(i.timeout))
	return
}

func (i idleTimeoutConn) Write(buf []byte) (n int, err error) {
	i.Conn.SetDeadline(time.Now().Add(i.timeout))
	n, err = i.Conn.Write(buf)
	// i.Conn.SetDeadline(time.Now().Add(i.timeout))
	return
}

type logAPI struct {
	stfu bool
}

func (l *logAPI) SetQuiet(stfu bool) {
	l.stfu = stfu
}

func (l logAPI) Print(f string, args ...interface{}) {
	if l.stfu {
		return
	}
	if len(args) == 0 {
		stdlog.Print(f + "\n")
	} else {
		stdlog.Printf(f+"\n", args...)
	}
}

func (l logAPI) Fatal(f string, args ...interface{}) {
	if len(args) == 0 {
		stdlog.Fatalf(f + "\n")
	} else {
		stdlog.Fatalf(f+"\n", args...)
	}
}

func (l logAPI) Error(f string, args ...interface{}) {
	if len(args) == 0 {
		stdlog.Print(f + "\n")
	} else {
		stdlog.Printf(f+"\n", args...)
	}
}
