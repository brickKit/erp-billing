// Command erp-billing runs the billing component alone (serve, migrate); the shells compile in billing.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/erp-billing/v3/billing"
)

func main() { launcher.Main(billing.Definition()) }
