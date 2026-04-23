package gateway

import "fmt"

type Gateway struct{}

func New() *Gateway {
	return &Gateway{}
}

func (g *Gateway) Check() error {
	return fmt.Errorf("plugin gateway is unavailable in current MVP")
}
