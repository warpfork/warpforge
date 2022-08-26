package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/json"
	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/otel"

	"github.com/warpfork/warpforge/pkg/dab"
	"github.com/warpfork/warpforge/pkg/logging"
	"github.com/warpfork/warpforge/pkg/plotexec"
	"github.com/warpfork/warpforge/wfapi"
)

var ferkCmdDef = cli.Command{
	Name:   "ferk",
	Usage:  "Starts a containerized environment for interactive use",
	Action: cmdFerk,
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name: "rootfs",
		},
		&cli.StringFlag{
			Name: "cmd",
		},
		&cli.BoolFlag{
			Name: "persist",
		},
		&cli.BoolFlag{
			Name: "no-interactive",
		},
		&cli.StringFlag{
			Name:    "plot",
			Aliases: []string{"p"},
		},
	},
}

const ferkPlotTemplate = `
{
        "inputs": {
                "rootfs": "catalog:min.warpforge.io/debian/rootfs:bullseye-1646092800:amd64"
        },
        "steps": {
                "ferk": {
                        "protoformula": {
                                "inputs": {
                                        "/": "pipe::rootfs",
                                        "/pwd": "mount:overlay:.",
                                },
                                "action": {
                                        "script": {
												"interpreter": "/bin/bash",
                                                "contents": [
													"echo 'APT::Sandbox::User \"root\";' > /etc/apt/apt.conf.d/01ferk",
													"echo 'Dir::Log::Terminal \"\";' >> /etc/apt/apt.conf.d/01ferk"
													"/bin/bash",
													],
												"network": true
                                        }
                                },
                                "outputs": {
									"out": {
										"from": "/out",
										"packtype": "tar"
									}
								}
                        }
                }
        },
        "outputs": {
			"out": "pipe:ferk:out"
		}
}
`

func cmdFerk(c *cli.Context) error {
	logger := logging.NewLogger(c.App.Writer, c.App.ErrWriter, c.Bool("json"), c.Bool("quiet"), c.Bool("verbose"))
	ctx := logger.WithContext(c.Context)

	traceProvider, err := configTracer(c.String("trace"))
	if err != nil {
		return fmt.Errorf("could not initialize tracing: %w", err)
	}
	defer traceShutdown(c.Context, traceProvider)
	tr := otel.Tracer(TRACER_NAME)
	ctx, span := tr.Start(ctx, c.Command.FullName())
	defer span.End()

	fsys := os.DirFS("/")

	wss, err := openWorkspaceSet(fsys)
	if err != nil {
		return err
	}

	plot := wfapi.Plot{}
	if c.String("plot") != "" {
		// plot was provided, load from file
		plot, err = dab.PlotFromFile(fsys, c.String("plot"))
		if err != nil {
			return fmt.Errorf("error loading plot from file %q: %s", c.String("plot"), err)
		}
	} else {
		// no plot provided, generate the basic default plot from json template
		_, err = ipld.Unmarshal([]byte(ferkPlotTemplate), json.Decode, &plot, wfapi.TypeSystem.TypeByName("Plot"))
		if err != nil {
			return fmt.Errorf("error parsing template plot: %s", err)
		}

		// convert rootfs input string to PlotInput
		// this requires additional quoting to be parsed correctly by ipld
		if c.String("rootfs") != "" {
			// custom value provided, override default
			rootfsStr := fmt.Sprintf("\"%s\"", c.String("rootfs"))
			rootfs := wfapi.PlotInput{}
			_, err = ipld.Unmarshal([]byte(rootfsStr), json.Decode, &rootfs, wfapi.TypeSystem.TypeByName("PlotInput"))
			if err != nil {
				return fmt.Errorf("error parsing rootfs input: %s", err)
			}
			plot.Inputs.Values["rootfs"] = rootfs
		}
	}

	// set command to execute
	if c.String("cmd") != "" {
		plot.Steps.Values["ferk"].Protoformula.Action = wfapi.Action{
			Exec: &wfapi.Action_Exec{
				Command: strings.Split(c.String("cmd"), " "),
			},
		}
	}

	if c.Bool("persist") {
		// set up a persistent directory on the host
		sandboxPath := wfapi.SandboxPath("/persist")
		port := wfapi.SandboxPort{
			SandboxPath: &sandboxPath,
		}
		plot.Steps.Values["ferk"].Protoformula.Inputs.Keys = append(plot.Steps.Values["ferk"].Protoformula.Inputs.Keys, port)
		plot.Steps.Values["ferk"].Protoformula.Inputs.Values[port] = wfapi.PlotInput{
			PlotInputSimple: &wfapi.PlotInputSimple{
				Mount: &wfapi.Mount{
					Mode:     "rw",
					HostPath: "./wf-persist",
				},
			},
		}
		// create the persist directory, if it does not exist
		err := os.MkdirAll("wf-persist", 0755)
		if err != nil {
			return fmt.Errorf("failed to create persist directory: %s", err)
		}
	}

	// set up interactive based on flags
	// disable memoization to force the formula to run
	config := wfapi.PlotExecConfig{
		FormulaExecConfig: wfapi.FormulaExecConfig{
			DisableMemoization: true,
			Interactive:        !c.Bool("no-interactive"),
		},
	}
	_, err = plotexec.Exec(ctx, wss, wfapi.PlotCapsule{Plot: &plot}, config)
	if err != nil {
		return err
	}

	return nil
}
