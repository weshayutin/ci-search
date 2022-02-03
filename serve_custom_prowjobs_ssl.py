#!/usr/bin/python3

from http.server import HTTPServer, BaseHTTPRequestHandler
import ssl
httpd = HTTPServer(('localhost', 8888), BaseHTTPRequestHandler)
httpd.socket = ssl.wrap_socket(
    httpd.socket,
    keyfile="localhost-key.pem",
    certfile='localhost.pem',
    server_side=True)
httpd.serve_forever()
