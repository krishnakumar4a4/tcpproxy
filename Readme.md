# TCP Proxy

A simple proxy server for HTTPS requests which can calculate the network upload and download speed in bytes/sec.

``./tcpproxy --help`` for usage  
``./tcpproxy --port 2345`` to listen on particular port with chained proxy assumed to be running localhost:3128  
``./tcpproxy --proxy --no-proxy`` to listen on particular port with no chained proxy  