// serk-pin prints the one-time PIN of a seeded staging position, for the
// development stack only (./run.sh pin <position>). The seed derives each PIN
// from SERK_SEED_PIN_SECRET (pinauth.DerivePIN), so it is never stored
// readable; this tool recomputes it. A PIN someone has already replaced at
// their first sign-in no longer works: that person chose a new one.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/serk-group/serk-construction/pkg/pinauth"
)

func main() {
	secret := flag.String("secret", "dev-only-seed-pin-secret-0123456789abcdefGHIJ", "the seed's PIN secret (the development default)")
	tenant := flag.String("tenant", "11111111-1111-4111-8111-111111111111", "tenant id")
	position := flag.String("position", "", "staging position, for example u-cfo")
	flag.Parse()
	if os.Getenv("SERK_ENV") == "production" || *position == "" {
		fmt.Fprintln(os.Stderr, "usage: serk-pin -position u-cfo   (development stack only)")
		os.Exit(2)
	}
	fmt.Println(pinauth.DerivePIN([]byte(*secret), *tenant, *position))
}
