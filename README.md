# netlisten

listen a net address and copy to a destination.

# Usage

```sh
netlisten [addr] to [dst]
  addr  ([tcp|udp|tcp6|udp4]://)?[hostname]:[port]
  dst   -|([tcp|udp|tcp6|udp4]://)?[hostname]:[port]
```

# Example

```sh
netlisten localhost:9091 & netlisten localhost:9090 to localhost:9091 &
sleep 1; echo "some thing" | nc --send-only localhost 9090
sleep 1;pkill listen
```

# Install

```sh
go get github.com/mh-cbon/netlisten
```
