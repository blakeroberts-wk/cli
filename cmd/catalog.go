package cmd

import (
	"time"

	"github.com/pkg/errors"
	managementClient "github.com/rancher/types/client/management/v3"
	"github.com/urfave/cli"
)

const (
	addCatalogDescription = `
Add a new catalog to the Rancher server

Example:
	# Add a catalog
	$ rancher catalog add foo https://my.catalog

	# Add a catalog and specify the branch to use
	$ rancher catalog add --branch awesomebranch foo https://my.catalog
`

	refreshCatalogDescription = `
Refresh a catalog on the Rancher server

Example:
	# Refresh a catalog
	$ rancher catalog refresh foo

	# Refresh multiple catalogs
	$ rancher catalog refresh foo bar baz

	# Refresh all catalogs
	$ rancher catalog refresh --all

	# Refresh is asynchronous unless you specify '--wait'
	$ rancher catalog refresh --all --wait --wait-timeout=60
`
)

type CatalogData struct {
	ID      string
	Catalog managementClient.Catalog
}

func CatalogCommand() cli.Command {
	catalogLsFlags := []cli.Flag{
		formatFlag,
		quietFlag,
	}

	return cli.Command{
		Name:   "catalog",
		Usage:  "Operations with catalogs",
		Action: defaultAction(catalogLs),
		Flags:  catalogLsFlags,
		Subcommands: []cli.Command{
			cli.Command{
				Name:        "ls",
				Usage:       "List catalogs",
				Description: "\nList all catalogs in the current Rancher server",
				ArgsUsage:   "None",
				Action:      catalogLs,
				Flags:       catalogLsFlags,
			},
			cli.Command{
				Name:        "add",
				Usage:       "Add a catalog",
				Description: addCatalogDescription,
				ArgsUsage:   "[NAME, URL]",
				Action:      catalogAdd,
				Flags: []cli.Flag{
					cli.StringFlag{
						Name:  "branch",
						Usage: "Branch from the url to use",
						Value: "master",
					},
				},
			},
			cli.Command{
				Name:        "delete",
				Usage:       "Delete a catalog",
				Description: "\nDelete a catalog from the Rancher server",
				ArgsUsage:   "[CATALOG_NAME/CATALOG_ID]",
				Action:      catalogDelete,
			},
			cli.Command{
				Name:        "refresh",
				Usage:       "Refresh catalog templates",
				Description: refreshCatalogDescription,
				ArgsUsage:   "[CATALOG_NAME/CATALOG_ID]...",
				Action:      catalogRefresh,
				Flags: []cli.Flag{
					cli.BoolFlag{
						Name:  "all",
						Usage: "Refresh all catalogs",
					},
					cli.BoolFlag{
						Name:  "wait,w",
						Usage: "Wait for catalog(s) to become active",
					},
					cli.IntFlag{
						Name:  "wait-timeout",
						Usage: "Wait timeout duration in seconds",
						Value: 0,
					},
				},
			},
		},
	}
}

func catalogLs(ctx *cli.Context) error {
	c, err := GetClient(ctx)
	if err != nil {
		return err
	}

	collection, err := c.ManagementClient.Catalog.List(defaultListOpts(ctx))
	if err != nil {
		return err
	}

	writer := NewTableWriter([][]string{
		{"ID", "ID"},
		{"NAME", "Catalog.Name"},
		{"URL", "Catalog.URL"},
		{"BRANCH", "Catalog.Branch"},
		{"KIND", "Catalog.Kind"},
	}, ctx)

	defer writer.Close()

	for _, item := range collection.Data {
		writer.Write(&CatalogData{
			ID:      item.ID,
			Catalog: item,
		})
	}

	return writer.Err()

}

func catalogAdd(ctx *cli.Context) error {
	if len(ctx.Args()) < 2 {
		return cli.ShowSubcommandHelp(ctx)
	}

	c, err := GetClient(ctx)
	if err != nil {
		return err
	}

	catalog := &managementClient.Catalog{
		Branch: ctx.String("branch"),
		Name:   ctx.Args().First(),
		Kind:   "helm",
		URL:    ctx.Args().Get(1),
	}

	_, err = c.ManagementClient.Catalog.Create(catalog)
	if err != nil {
		return err
	}

	return nil
}

func catalogDelete(ctx *cli.Context) error {
	if len(ctx.Args()) < 1 {
		return cli.ShowSubcommandHelp(ctx)
	}

	c, err := GetClient(ctx)
	if err != nil {
		return err
	}

	for _, arg := range ctx.Args() {
		resource, err := Lookup(c, arg, "catalog")
		if err != nil {
			return err
		}

		catalog, err := c.ManagementClient.Catalog.ByID(resource.ID)
		if err != nil {
			return err
		}

		err = c.ManagementClient.Catalog.Delete(catalog)
		if err != nil {
			return err
		}
	}
	return nil
}

func catalogRefresh(ctx *cli.Context) error {
	if len(ctx.Args()) < 1 && !ctx.Bool("all") {
		return cli.ShowSubcommandHelp(ctx)
	}

	c, err := GetClient(ctx)
	if err != nil {
		return err
	}

	var collection *managementClient.CatalogCollection

	if ctx.Bool("all") {
		opts := baseListOpts()

		// just interested in the actions, not the actual catalogs
		opts.Filters["limit"] = 0

		collection, err = c.ManagementClient.Catalog.List(opts)
		if err != nil {
			return err
		}

		err = c.ManagementClient.Catalog.CollectionActionRefresh(collection)
		if err != nil {
			return err
		}

	} else {

		for _, arg := range ctx.Args() {

			resource, err := Lookup(c, arg, "catalog")
			if err != nil {
				return err
			}

			catalog, err := c.ManagementClient.Catalog.ByID(resource.ID)
			if err != nil {
				return err
			}

			// collect the refreshing catalogs in case we need to wait for them later
			collection.Data = append(collection.Data, *catalog)

			err = c.ManagementClient.Catalog.ActionRefresh(catalog)
			if err != nil {
				return err
			}
		}

	}

	if ctx.Bool("wait") {

		// refresh timeout channel
		channel := make(chan error, 1)

		// start a goroutine that waits for each catalog's state to become active
		go func() {
			for _, catalog := range collection.Data {

				resource, err := Lookup(c, catalog.Name, "catalog")
				if err != nil {
					channel <- err
				}

				catalog, err := c.ManagementClient.Catalog.ByID(resource.ID)
				if err != nil {
					channel <- err
				}

				for catalog.State != "active" {
					catalog, err = c.ManagementClient.Catalog.ByID(resource.ID)
					if err != nil {
						channel <- err
					}
				}

			}
			channel <- nil
		}()

		// if a wait timeout was set, include an additional switch case for it
		timeout := ctx.Int("wait-timeout")
		if timeout > 0 {
			select {
			case err = <-channel:
				return err
			case <-time.After(time.Duration(timeout) * time.Second):
				return errors.New("catalog: timed out waiting for catalog refresh")
			}
		}

		// otherwise wait indefinitely for the channel
		select {
		case err = <-channel:
			return err
		}

	}

	return nil
}
