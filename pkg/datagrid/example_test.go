package datagrid_test

import (
	"context"
	"io"

	"github.com/a-h/templ"
	"monks.co/pkg/datagrid"
)

func ExampleTable() {
	type person struct {
		ID   string
		Name string
		Team string
	}
	rows := []person{
		{ID: "ada", Name: "Ada Lovelace", Team: "Research"},
		{ID: "grace", Name: "Grace Hopper", Team: "Navy"},
	}
	opts := datagrid.Options[person]{
		ID:       "people",
		PageSize: 25,
		Columns: []datagrid.Column[person]{
			datagrid.TextColumn("name", "Name", func(row person) string { return row.Name }),
			datagrid.TextColumn("team", "Team", func(row person) string { return row.Team }),
		},
		RowID: func(row person) string { return row.ID },
	}

	// Render datagrid.Head() once in the document head and this component in
	// the body. Rendering returns configuration errors instead of panicking.
	_ = datagrid.Head().Render(context.Background(), io.Discard)
	_ = datagrid.Table(opts, rows).Render(context.Background(), io.Discard)
	_, _ = datagrid.QueryCodecForOptions(opts)
}

func ExampleShell() {
	// Shell is the low-level path for a caller-owned or dynamically populated
	// table. HeaderCell, Row, and Cell produce the same annotations used by
	// Table; after inserting rows in the browser, call element.refresh().
	table := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<table id="remote-people-table" class="datagrid-table"><thead><tr>`); err != nil {
			return err
		}
		if err := datagrid.HeaderCell(datagrid.HeaderCellProps{
			Column: "name",
			Label:  "Name",
		}).Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</tr></thead><tbody></tbody></table>`)
		return err
	})
	ctx := templ.WithChildren(context.Background(), table)
	_ = datagrid.Shell(datagrid.ShellProps{ID: "remote-people"}).Render(ctx, io.Discard)
}
