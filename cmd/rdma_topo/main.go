// Use and distribution licensed under the Apache license version 2.

// Command rdma_topo prints the NVIDIA ConnectX GPU Direct topology
// for the local system. It is a partial Go port of the upstream
// rdma-core kernel-boot/rdma_topo Python script, currently the
// "topology" subcommand only.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jaypipes/ghw"

	rdmatopo "github.com/dims/rdma_topo"
	"github.com/dims/rdma_topo/internal/report"
)

const usage = `Usage: rdma_topo <command> [flags]

Commands:
  topology, topo   Print the NVCX (ConnectX GPU Direct) topology

Run 'rdma_topo <command> -h' for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "topology", "topo":
		if err := runTopology(os.Args[2:]); err != nil {
			fail(err)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "rdma_topo: unknown command %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func fail(err error) {
	if errors.Is(err, rdmatopo.ErrNoCXDMA) ||
		errors.Is(err, rdmatopo.ErrVPDPermissionDenied) ||
		errors.Is(err, rdmatopo.ErrSysfsUnavailable) {
		// Match the Python "E: ..." prefix and exit code 100 for
		// CommandError, rdma_topo:765-766.
		fmt.Fprintf(os.Stderr, "E: %s\n", err)
		os.Exit(100)
	}
	fmt.Fprintf(os.Stderr, "rdma_topo: %s\n", err)
	os.Exit(1)
}

func runTopology(args []string) error {
	fs := flag.NewFlagSet("topology", flag.ExitOnError)
	jsonOut := fs.Bool("j", false, "Output in machine readable JSON format")
	fs.BoolVar(jsonOut, "json", false, "Output in machine readable JSON format")
	chroot := fs.String("chroot", "", "Alternate filesystem root for sysfs (testing)")
	fs.Parse(args)

	// Start from the env-derived context so GHW_CHROOT applies,
	// then layer --chroot (which takes precedence) and disable
	// topology detection for cheaper enumeration.
	ctx := ghw.ContextFromEnv()
	if *chroot != "" {
		ctx = ghw.WithChroot(*chroot)(ctx)
	}
	ctx = ghw.WithDisableTopology()(ctx)

	topo, err := rdmatopo.Discover(ctx)
	if errors.Is(err, rdmatopo.ErrNoCXDMA) ||
		errors.Is(err, rdmatopo.ErrVPDPermissionDenied) ||
		errors.Is(err, rdmatopo.ErrSysfsUnavailable) {
		return err
	}
	if err != nil {
		// Soft errors: surface on stderr but still emit the
		// complexes that did assemble.
		fmt.Fprintf(os.Stderr, "rdma_topo: %s\n", err)
	}

	if *jsonOut {
		return report.JSON(ctx, os.Stdout, topo)
	}
	return report.Human(ctx, os.Stdout, topo)
}
