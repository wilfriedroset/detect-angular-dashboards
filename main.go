package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/grafana/detect-angular-dashboards/api/gcom"
	"github.com/grafana/detect-angular-dashboards/api/grafana"
	"github.com/grafana/detect-angular-dashboards/logger"
)

const envGrafanaToken = "GRAFANA_TOKEN"

func _main() error {
	verboseFlag := flag.Bool("v", false, "verbose output")
	flag.Parse()

	log := &logger.Logger{Verbose: *verboseFlag}

	token := os.Getenv(envGrafanaToken)
	if token == "" {
		return fmt.Errorf("missing env var %q", envGrafanaToken)
	}
	grafanaURL := grafana.DefaultBaseURL
	if flag.NArg() >= 1 {
		grafanaURL = flag.Arg(0)
	}
	log.Logf("Detecting Angular dashboards for %q", grafanaURL)

	ctx := context.Background()
	grCl := grafana.NewAPIClient(grafanaURL, token)
	gcomCl := gcom.NewAPIClient()

	// Determine if plugins are angular.
	// This can be done from frontendsettings (faster and works with private plugins, but only works with >= 10.1.0)
	// or from GCOM (slower, but always available, but public plugins only)
	angularDetected := map[string]bool{}
	frontendSettings, err := grCl.GetFrontendSettings(ctx)
	if err != nil {
		return fmt.Errorf("get frontend settings: %w", err)
	}

	// Determine if we should use GCOM or frontendsettings
	var useGCOM bool
	// Get any key and see if AngularDetected is present or not.
	// From Grafana 10.1.0, it will always be present.
	// Before Grafana 10.1.0, it's always nil as it's not present in the body.
	for _, p := range frontendSettings.Panels {
		useGCOM = p.AngularDetected == nil
		break
	}
	if useGCOM {
		// Fall back to GCOM (< 10.1.0)
		log.Verbosef("Using GCOM to find Angular plugins")
		log.Logf("(WARNING, dependencies on private plugins won't be flagged)")
		plugins, err := grCl.GetPlugins(ctx)
		if err != nil {
			return fmt.Errorf("get plugins: %w", err)
		}
		for _, p := range plugins {
			if p.Info.Version == "" {
				continue
			}
			angularDetected[p.ID], err = gcomCl.GetAngularDetected(ctx, p.ID, p.Info.Version)
			if err != nil {
				return fmt.Errorf("get angular detected: %w", err)
			}
		}
	} else {
		log.Verbosef("Using frontendsettings to find Angular plugins")
		for pluginID, meta := range frontendSettings.Panels {
			angularDetected[pluginID] = *meta.AngularDetected
		}
		for _, meta := range frontendSettings.Datasources {
			angularDetected[meta.Type] = *meta.AngularDetected
		}
	}

	// Debug
	for p, isAngular := range angularDetected {
		log.Verbosef("Plugin %q angular %t", p, isAngular)
	}

	// Map ds name -> ds plugin id, to resolve legacy dashboards that have ds name
	apiDs, err := grCl.GetDatasourcePluginIDs(ctx)
	if err != nil {
		return fmt.Errorf("get datasource plugin ids: %w", err)
	}
	datasourcePluginIDs := make(map[string]string, len(apiDs))
	for _, ds := range apiDs {
		datasourcePluginIDs[ds.Name] = ds.Type
		log.Verbosef("Datasource %q plugin ID %q", ds.Name, ds.Type)
	}

	dashboards, err := grCl.GetDashboards(ctx, 1)
	if err != nil {
		return fmt.Errorf("get dashboards: %w", err)
	}
	for _, d := range dashboards {
		var detectionMessages []string
		dashboard, err := grCl.GetDashboard(ctx, d.UID)
		if err != nil {
			return fmt.Errorf("get dashboard %q: %w", d.UID, err)
		}
		for _, p := range dashboard.Panels {
			// Check panel
			if angularDetected[p.Type] {
				detectionMessages = append(detectionMessages, fmt.Sprintf("Found angular panel %q", p.Type))
			}

			// Check datasource
			var dsPlugin string
			// The datasource field can either be a string (old) or object (new)
			if p.Datasource == nil || p.Datasource == "" {
				continue
			}
			if dsName, ok := p.Datasource.(string); ok {
				dsPlugin = datasourcePluginIDs[dsName]
			} else if ds, ok := p.Datasource.(grafana.PanelDatasource); ok {
				dsPlugin = ds.Type
			} else {
				return fmt.Errorf("unknown unmarshaled datasource type %T", p.Datasource)
			}
			if angularDetected[dsPlugin] {
				detectionMessages = append(detectionMessages, fmt.Sprintf("Found angular datasource %q", dsPlugin))
			}
		}

		// Determine absolute dashboard URL
		dashboardAbsURL, err := url.JoinPath(strings.TrimSuffix(grafanaURL, "/api"), d.URL)
		if err != nil {
			// Silently ignore errors
			dashboardAbsURL = ""
		}

		// Print output
		checkMessage := fmt.Sprintf("Checking dashboard %q %q", d.Title, dashboardAbsURL)
		if len(detectionMessages) > 0 {
			log.Logf(checkMessage)
			for _, msg := range detectionMessages {
				log.Logf(msg)
			}
		} else {
			log.Verbosef(checkMessage)
		}
	}
	return nil
}

func main() {
	if err := _main(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", err)
	}
}
