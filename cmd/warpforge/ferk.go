package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/json"
	"github.com/urfave/cli/v2"
	"github.com/warpfork/warpforge/pkg/logging"
	"github.com/warpfork/warpforge/pkg/plotexec"
	"github.com/warpfork/warpforge/wfapi"
)

var ferkCmdDef = cli.Command{
	Name:   "ferk",
	Usage:  "Idk yet",
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
	},
}

const ferkPlotTemplate = `
{
        "inputs": {
                "rootfs": "catalog:warpsys.org/bootstrap-rootfs:bullseye-1646092800:amd64"
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

/*
const ferkPlotTemplate = `
{
        "inputs": {
                "rootfs": "literal:none"
        },
        "steps": {
                "ferk": {
                        "protoformula": {
                                "inputs": {
                                        "/pwd": "mount:overlay:.",
                                        "/persist": "mount:rw:wf-persist",
										"/pkg/busybox": "catalog:busybox.net/busybox:v1.35.0:amd64"
										"$PATH": "literal:/pkg/busybox/bin:/pkg/busybox/sbin:/pkg/busybox/usr/bin:/pkg/busybox/usr/sbin"
                                },
                                "action": {
                                        "exec": {
                                                "command": ["/pkg/busybox/bin/sh"],
												"network": true
                                        }
                                },
                                "outputs": {}
                        }
                }
        },
        "outputs": {}
}
`
*/

func cmdFerk(c *cli.Context) error {
	logger := logging.NewLogger(c.App.Writer, c.App.ErrWriter, c.Bool("verbose"))

	wss, err := openWorkspaceSet()
	if err != nil {
		return err
	}

	// generate the basic default plot from json template
	plot := wfapi.Plot{}
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

	// run the plot in interactive mode
	config := wfapi.PlotExecConfig{
		Interactive: true,
	}
	_, err = plotexec.Exec(wss, plot, config, logger)
	if err != nil {
		return err
	}

	return nil
}
