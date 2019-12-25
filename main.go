package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

var rootCmd *cobra.Command
var port int
var proxyIp string
var proxyPort int
var noProxy bool

var totUploadBytes int64
var totUploadDuration int64

var totDownloadBytes int64
var totDownloadDuration int64

type Conf struct {
	Port           int      `json:port`
	Proxyip        string   `json:proxyip`
	Proxyport      int      `json:proxyport`
	Alloweddomains []string `json:alloweddomains`
}

var conf Conf

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	c, err := readConf()
	rootCmd = &cobra.Command{
		Use:   "tunproxy",
		Short: "A transparent proxy",
		Long:  "A transparent proxy server with support to be chained with downstream proxy and rate calculator",
		Run: func(cmd *cobra.Command, args []string) {
			start()
		},
	}
	localPort := 0
	localProxyIp := ""
	localProxyPort := 0
	if err == nil {
		localPort = c.Port
		localProxyIp = c.Proxyip
		localProxyPort = c.Proxyport
	}
	rootCmd.Flags().IntVarP(&port, "port", "p", localPort, "Port")
	rootCmd.Flags().StringVar(&proxyIp, "proxy-ip", localProxyIp, "Downstream proxy ip address if exists")
	rootCmd.Flags().IntVar(&proxyPort, "proxy-port", localProxyPort, "Downstream proxy port")
	rootCmd.Flags().BoolVar(&noProxy, "no-proxy", false, "No downstream proxy")
	if port == 0 {
		rootCmd.MarkFlagRequired("port")
	}
}

func start() {
	fmt.Println("port: ", port)
	fmt.Println("no-proxy", noProxy)
	if (port == 0) || (!noProxy && (proxyIp == "" || proxyPort == 0)) {
		fmt.Println("missing params port/proxy-ip/proxy-port")
		return
	}
	if !noProxy {
		fmt.Println("downstream proxy-ip : ", proxyIp)
		fmt.Println("downstream proxy-port : ", proxyPort)
	}
	fmt.Println("allowed domains: ", conf.Alloweddomains)

	l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Fatalf("unable to listen on port: %v, err: %v", port, err.Error())
		return
	}
	defer l.Close()
	go func() {
		for {
			time.Sleep(time.Second * 5)
			fmt.Println("Number of go routines: ", runtime.NumGoroutine())
			if totUploadDuration == 0 || totDownloadDuration == 0 {
				continue
			}
			uploadSpeed := float32(totUploadBytes) / (float32(totUploadDuration) / (1000000000))
			downloadSpeed := float32(totDownloadBytes) / (float32(totDownloadDuration) / (1000000000))
			fmt.Printf("upload speed n: %v, d: %v, %v bytes/sec\n", totUploadBytes, float32(totUploadDuration)/(1000000000), int64(uploadSpeed))
			fmt.Printf("download speed n: %v, d: %v, %v bytes/sec\n", totDownloadBytes, float32(totDownloadDuration)/(1000000000), int64(downloadSpeed))
		}
	}()

	for {
		tcpConn, err := l.Accept()
		if err != nil {
			fmt.Println("error tcp accept: ", err)
			return
		}
		fmt.Println("new connection accepted")

		go func() {
			var domainName string
			var connectBytes []byte
			var url string
			connect, connectBuf := isConnectRequest(tcpConn)
			if connect {
				url, connectBytes = parseConnect(tcpConn, connectBuf)
				fmt.Println("url: ", url)
				urlParts := strings.Split(url, ":")
				domainName = urlParts[0]
			} else {
				url, connectBytes = parseHTTPURL(tcpConn, connectBuf)
				fmt.Println("url: ", url)
				domainName = url
				url = fmt.Sprintf("%s:%s", domainName, "80")
			}
			var isAllowedDomain bool
			for _, dName := range conf.Alloweddomains {
				if dName == domainName {
					isAllowedDomain = true
					break
				}
			}

			if !isAllowedDomain {
				fmt.Println(fmt.Sprintf("This domain %v is blocked", domainName))
				return
			}

			var isProxyEnabled bool
			if (proxyIp != "" || proxyPort != 0) && !noProxy {
				isProxyEnabled = true
			}
			if isProxyEnabled {
				url = fmt.Sprintf("%v:%v", proxyIp, proxyPort)
				fmt.Println("connected to proxy url:", url)
			}
			targetConn, err := net.Dial("tcp", url)
			if err != nil {
				fmt.Println("Unable to connect to target host: ", url, " err: ", err)
				return
			}
			if connect && isProxyEnabled {
				targetConn.Write(connectBytes)
				BUFSIZE := 1024 * 5
				targetConnBuf := make([]byte, BUFSIZE)
				n, err := targetConn.Read(targetConnBuf)
				if n > 0 {
					tcpConn.Write(targetConnBuf[:n])
				}
				if err != nil {
					if err == io.EOF {
						fmt.Println("reading from tcp conn, EOF")
					}
					fmt.Println("tcpConn Read err: ", err)
				}
			}
			if !connect {
				_, err := targetConn.Write(connectBytes)
				if err != nil {
					fmt.Println("error while writing http bytes to targetConn: ", err)
				}
			}
			proxy(tcpConn, targetConn)
		}()
	}
}

func proxy(tcpConn, targetConn net.Conn) {
	go func() {
		for {
			a := time.Now()
			tcpConn.SetDeadline(a.Add(10 * time.Second))
			n, err := io.Copy(targetConn, tcpConn)
			if n > 0 {
				sumUploadStats(int(n), a)
			}
			if err != nil {
				fmt.Println("error while copying from tcpConn", err)
				break
			}
		}
	}()
	go func() {
		for {
			a := time.Now()
			targetConn.SetDeadline(a.Add(10 * time.Second))
			n, err := io.Copy(tcpConn, targetConn)
			if n > 0 {
				sumDownloadStats(int(n), a)
			}
			if err != nil {
				fmt.Println("error while copying from targetConn", err)
				break
			}
		}
	}()
}

func resetCRLF(cr1, cr2, lf1, lf2 *bool) {
	*cr1 = false
	*cr2 = false
	*lf1 = false
	*lf2 = false
}

func parseConnect(tcpConn io.ReadWriter, connectBytes []byte) (string, []byte) {
	cr1 := false
	cr2 := false
	lf1 := false
	lf2 := false
	BUFSIZE := 100
	totReqBytes := connectBytes

	for {
		buf := make([]byte, BUFSIZE)
		n, err := tcpConn.Read(buf)
		if err != nil {
			fmt.Println("Read all err: ", err)
		}
		if n < BUFSIZE {
			totReqBytes = append(totReqBytes, buf[:n]...)
			break
		}
		totReqBytes = append(totReqBytes, buf[:n]...)
		// fmt.Println("data read: ", string(buf[:n]))
		for _, b := range buf[:n] {
			if lf2 && b == 10 {
				resetCRLF(&cr1, &cr2, &lf1, &lf2)
				break
			} else if lf2 {
				resetCRLF(&cr1, &cr2, &lf1, &lf2)
			}
			if cr2 && b == 13 {
				lf2 = true
				continue
			} else if cr2 {
				resetCRLF(&cr1, &cr2, &lf1, &lf2)
			}
			if lf1 && b == 10 {
				cr2 = true
				continue
			} else if lf1 {
				resetCRLF(&cr1, &cr2, &lf1, &lf2)
			}
			if !cr1 && b == 13 {
				lf1 = true
				cr1 = true
				continue
			}
		}
	}
	fmt.Println("conn request: ", string(totReqBytes))
	tcpConn.Write(append([]byte("HTTP/1.1 200 Connection established"), []byte{10, 13, 10, 13}...))
	totReqString := string(totReqBytes)
	lines := strings.Split(totReqString, "\n")
	urls := strings.Split(lines[0], " ")
	fmt.Println("connect len: ", len(totReqBytes), string(totReqBytes[:7]))
	return urls[1], totReqBytes
}

func isConnectRequest(tcpConn io.ReadWriter) (bool, []byte) {
	buf := make([]byte, 7)
	for {
		n, err := io.ReadAtLeast(tcpConn, buf, 7)
		if err != nil {
			fmt.Println("connect read err: ", err)
			return false, buf[:n]
		}
		if n == 7 {
			if string(buf) == "CONNECT" {
				return true, buf
			} else {
				return false, buf
			}
		}
	}
}

func parseHTTPURL(tcpConn io.ReadWriter, connectBytes []byte) (string, []byte) {
	BUFSIZE := 100
	totReqBytes := connectBytes
	var hostHeader bool
	for {
		buf := make([]byte, BUFSIZE)
		n, err := tcpConn.Read(buf)
		if err != nil {
			fmt.Println("Read all err: ", err)
		}
		if n < BUFSIZE {
			totReqBytes = append(totReqBytes, buf[:n]...)
			break
		}
		totReqBytes = append(totReqBytes, buf[:n]...)
		idx := strings.LastIndex(string(totReqBytes), "Host: ")
		if idx != -1 {
			hostHeader = true
		}
		if hostHeader {
			remHdrs := strings.SplitAfterN(string(totReqBytes), "\r\n", 2)
			if len(remHdrs) > 1 {
				hdrs := strings.Split(string(totReqBytes), "\r\n")
				hostHdrSplits := strings.Split(hdrs[1], "www.")
				return string(hostHdrSplits[1]), totReqBytes
			}
		}
	}
	return "", totReqBytes
}

func readConf() (*Conf, error) {
	data, err := ioutil.ReadFile("conf.json")
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &conf)
	if err != nil {
		return nil, err
	}
	fmt.Println("conf read", conf)
	return &conf, nil
}

func sumUploadStats(n int, a time.Time) {
	d := time.Now().Sub(a)
	atomic.AddInt64(&totUploadBytes, int64(n))
	atomic.AddInt64(&totUploadDuration, int64(d))
}

func sumDownloadStats(n int, a time.Time) {
	d := time.Now().Sub(a)
	atomic.AddInt64(&totDownloadBytes, int64(n))
	atomic.AddInt64(&totDownloadDuration, int64(d))
}
