//go:build ignore

// Loopback-only lab client for the Admin API used by Console's OIDC form.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/minio/madmin-go/v3"
)

func main() {
	endpoint := os.Getenv("LAB_SERVER")
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil || host != "127.0.0.1" {
		panic("LAB_SERVER must use IPv4 loopback")
	}
	a, err := madmin.New(endpoint, os.Getenv("LAB_USER"), os.Getenv("LAB_PASSWORD"), false)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "add" {
		restart, err := a.AddOrUpdateIDPConfig(ctx, "openid", "local154", "enable=on client_id=local154 client_secret=local154-placeholder config_url="+os.Getenv("LAB_OIDC_URL"), false)
		fmt.Printf("add restart=%v err=%v\n", restart, err)
		if err != nil {
			os.Exit(1)
		}
		return
	}
	users, err := a.ListUsers(ctx)
	fmt.Printf("list_users count=%d err=%v\n", len(users), err)
	if err != nil {
		os.Exit(1)
	}
}
