// Command service executa a API, o consumidor SQS e os workers, conforme APP_ROLES.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"jungle-gaming-backend/internal/bootstrap"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		address := os.Getenv("HTTP_ADDRESS")
		if address == "" {
			address = ":8080"
		}
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			os.Exit(1)
		}
		client := &http.Client{Timeout: 4 * time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/health/ready")
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	app, err := bootstrap.New(os.Getenv, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.Run() // bloqueia até SIGINT/SIGTERM e executa o encerramento ordenado
}
