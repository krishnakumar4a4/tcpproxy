package main

import (
	"fmt"
	"io"
	"os"
	"io/ioutil"
	"log"
	"net"
	"strings"
	"time"
	"runtime"
	"github.com/spf13/cobra"
	"encoding/json"
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
	Port int `json:targetport`
	Proxyip string `json:proxyip`
	Proxyport int `json:proxyport`
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
	readConf()
	rootCmd = &cobra.Command {
		Use: "tunproxy",
		Short: "A Proxy coupled with ssh reverse tunnel",
		Long: "A Local proxy server coupled with SSH reverse tunnel to provide internet access to remote machines using local proxy server",
		Run: func(cmd *cobra.Command, args []string) {
			start()
		},
	}
	rootCmd.Flags().IntVarP(&port, "port","p",3129,"Tunneled listening port on remote server")

	rootCmd.Flags().StringVarP(&proxyIp, "proxy-ip","","","Proxy ip address to be used")
	rootCmd.Flags().IntVarP(&proxyPort, "proxy-port","",3128,"Proxy port to be used")
	rootCmd.Flags().BoolVarP(&noProxy, "no-proxy","",false,"If wanted to disbale default proxy(localhost:3128) configuration")

	rootCmd.MarkFlagRequired("port")
}

func start() {
	l, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Fatal("unable to register tcp forward: ", err)
		return
	}
	defer l.Close()

	fmt.Println("read started")
	for {
		tcpConn, err := l.Accept()
		if err != nil {
			fmt.Println("error tcp accept: ", err)
			return
		}
		fmt.Println("connection accepted")

		fmt.Println("Number of go routines running after accept ", runtime.NumGoroutine())
	
		go func() {
			waitChan := make(chan int)
			url, connectBytes := parseConnect(tcpConn)
			fmt.Println("url: ", url)
			urlParts := strings.Split(url, ":")
			domainName := urlParts[0]
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
				url = fmt.Sprintf("%v:%v",proxyIp, proxyPort)
				fmt.Println("connected to proxy url:", url)
			}
			targetConn, err := net.Dial("tcp", url)
			if err != nil {
				fmt.Println("Unable to connect to target host: ", url)
				return
			}
			if isProxyEnabled {
				targetConn.Write(connectBytes)
				BUFSIZE := 1024 * 5
				targetConnBuf := make([]byte, BUFSIZE)
				targetConn.Read(targetConnBuf)
			}
			proxy(tcpConn, targetConn, waitChan)
			<- waitChan
			fmt.Println("Ended go routine for url ", url)
			fmt.Println("Number of go routines running after connection", runtime.NumGoroutine()-1)
		}()
	}
}

func readConf() error {
	data, err := ioutil.ReadFile("conf.json")
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, &conf)
	if err != nil {
		return err
	}
	fmt.Println("conf read", conf)
	if port == 0 {
		port = conf.Port
	}
	if proxyIp == "" {
		proxyIp = conf.Proxyip
	}
	if proxyPort == 0 {
		proxyPort = conf.Proxyport
	}
	return nil
}

func sumUploadStats(n int, a time.Time) {
	d := time.Now().Sub(a)
	totUploadBytes += int64(n)
	totUploadDuration += int64(d)
}

func sumDownloadStats(n int, a time.Time) {
	d := time.Now().Sub(a)
	totDownloadBytes += int64(n)
	totDownloadDuration += int64(d)
}

func proxy(tcpConn, targetConn net.Conn, waitChan chan int) {
	BUFSIZE := 1024 * 5
	go func() {
		for {
			tcpConnBuf := make([]byte, BUFSIZE)
			fmt.Println("reading from ssh conn")
			n, err := tcpConn.Read(tcpConnBuf)
			// fmt.Printf("tcpConnBuf: %v, size: %v", hex.EncodeToString(tcpConnBuf[:n]), n)
			if n != 0 {
				fmt.Println("wrote to target conn")
				// fmt.Println("tcpConn data", string(tcpConnBuf))
				a := time.Now()
				targetConn.Write(tcpConnBuf[:n])
				sumUploadStats(n, a)
			}
			if err != nil {
				if err == io.EOF {
					fmt.Println("reading from ssh conn, EOF")
				}
				fmt.Println("Read all err: ", err)
				break
			}
		}
	}()
	go func() {
		for {
			targetConnBuf := make([]byte, BUFSIZE)
			fmt.Println("reading from target conn")

			// Timeout implementation for read
			timeoutChan := make(chan int)
			timer := time.NewTimer(10 * time.Second)
			go func() {
				timeout(timer, targetConn, timeoutChan)
			}()

			// Reading from target
			a := time.Now()
			n, err := targetConn.Read(targetConnBuf)
			fmt.Println("finishing timer")
			sumDownloadStats(n, a)

			// Clean up timer, in case succefully read from target in time
			timer.Stop()
			go func() { 
				defer func() {
					if r := recover(); r != nil {
						fmt.Println("timeout channel blew, recovering and doing nothing as I dont care")
					}
				}()
				timeoutChan <- 1 
			}()

			// fmt.Printf("targetConnBuf: %v, size: %v", hex.EncodeToString(targetConnBuf[:n]), n)
			fmt.Println("Number of go routines running ", runtime.NumGoroutine())
			
			// Read some non-zero bytes from target
			if n != 0 {
				fmt.Println("wrote to ssh conn")
				// fmt.Println("targetConn data", string(targetConnBuf))
				tcpConn.Write(targetConnBuf[:n])
			}

			// Break on error
			if err != nil {
				if err == io.EOF {
					fmt.Println("reading from target conn, EOF")
				}
				fmt.Println("Read all err: ", err)

				// when target connection is done, we will signal to bring down this go routine to parent
				waitChan <- 1
				break
			}
		}
	}()
}

func timeout(timer *time.Timer, targetConn net.Conn, timeoutChan chan int) {
	fmt.Println("started timeout")
	select  {
		case a := <- timer.C:
			err := targetConn.Close()
			fmt.Println("ended timeout closing targetConn with err ", err, a)
			var uploadSpeed float32
			var downloadSpeed float32
			uploadSpeed = float32(totUploadBytes)/(float32(totUploadDuration)/(1000000000))
			downloadSpeed = float32(totDownloadBytes)/(float32(totDownloadDuration)/(1000000000))
			fmt.Printf("upload speed n: %v, d: %v, %v bytes/sec\n", totUploadBytes, float32(totUploadDuration)/(1000000000), int64(uploadSpeed))
			fmt.Printf("download speed n: %v, d: %v, %v bytes/sec\n", totDownloadBytes, float32(totDownloadDuration)/(1000000000), int64(downloadSpeed))
			close(timeoutChan)
		case <- timeoutChan:
			fmt.Println("ended timeout casually")
	}
}

func resetCRLF(cr1, cr2, lf1, lf2 *bool) {
	*cr1 = false
	*cr2 = false
	*lf1 = false
	*lf2 = false
}

func parseConnect(tcpConn io.ReadWriter) (string, []byte) {
	cr1 := false
	cr2 := false
	lf1 := false
	lf2 := false
	BUFSIZE := 100
	totReqBytes := []byte{}

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
	return urls[1], totReqBytes
}