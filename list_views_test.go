package main

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func resetListViewTest(t *testing.T, command string) {
	t.Helper()
	resetTransformConfig(t)
	oldCommand := invokedCommandName
	oldFetch := resolverListFetch
	t.Cleanup(func() {
		invokedCommandName = oldCommand
		resolverListFetch = oldFetch
		viper.Set("rsh-output-format", nil)
		viper.Set("table-columns-auto", nil)
		viper.Set("table-priority-column", nil)
		viper.Set("table-link-column", nil)
		viper.Set("table-link-url-key", nil)
	})
	invokedCommandName = command
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		t.Fatalf("unexpected folder lookup on %s", listPath)
		return resolverListResult{}, nil
	}
	viper.Set("rsh-output-format", "table")
	viper.Set("table-columns-auto", false)
	viper.Set("table-priority-column", "")
	viper.Set("table-link-column", "")
	viper.Set("table-link-url-key", "")
}

// listViewCase captures one command's curated view against a realistic
// sample row (field sets mirror live API responses).
type listViewCase struct {
	command  string
	itemsKey string
	row      map[string]interface{}
	columns  string
	linkURL  string                 // expected table-link-url-key ("" = no link)
	cells    map[string]interface{} // expected derived/mirrored cells
	names    []nameCacheEntry       // id → name entries the resolver returns (nil = no lookup expected)
}

func listViewCases() []listViewCase {
	return []listViewCase{
		{
			command:  "list-budgets",
			itemsKey: "budgets",
			row: map[string]interface{}{
				"id": "zwW1rPpV43nHkE0zTJoj", "budgetName": "GCP dev — App Engine",
				"owner": "someone@example.com", "amount": int64(4900),
				"currency": "USD", "currentUtilization": 2737.09, "riskStatus": "onTrack",
				"createTime": int64(1786400706138), "updateTime": int64(1786400706138),
				"url": "https://console.example.com/budgets/zwW1rPpV43nHkE0zTJoj",
			},
			columns: "budget name,owner,amount,spend to date,risk,updated (UTC)",
			linkURL: "url",
			cells: map[string]interface{}{
				"budget name":   "GCP dev — App Engine",
				"spend to date": 2737.09,
				"risk":          "onTrack",
				"updated (UTC)": int64(1786400706138),
			},
		},
		{
			command:  "list-allocations",
			itemsKey: "allocations",
			row: map[string]interface{}{
				"id": "aWUB9gRAEkCqcdK2PCOh", "name": "CloudDiagrams",
				"owner": "someone@example.com", "type": "custom", "allocationType": "single",
				"folderId": "root", "createTime": int64(1787051321353), "updateTime": int64(1787051321353),
				"urlUI": "https://console.example.com/allocations/aWUB9gRAEkCqcdK2PCOh",
			},
			columns: "name,owner,type,folder,updated (UTC)",
			linkURL: "urlUI",
			cells:   map[string]interface{}{"folder": "", "updated (UTC)": int64(1787051321353)},
		},
		{
			command:  "list-anomalies",
			itemsKey: "anomalies",
			row: map[string]interface{}{
				"id": "c72f2bda", "serviceName": "Cursor", "severityLevel": "warning",
				"costOfAnomaly": 116.39, "platform": "cursor", "status": "active",
				"startTime": int64(1786924800000), "acknowledged": false,
			},
			columns: "service,severity,anomaly cost,platform,status,started (UTC)",
			cells: map[string]interface{}{
				"service": "Cursor", "severity": "warning",
				"anomaly cost": 116.39, "started (UTC)": int64(1786924800000),
			},
		},
		{
			command:  "list-alerts",
			itemsKey: "alerts",
			row: map[string]interface{}{
				"id": "beNMYP7z8f2C8Qzfx4aJ", "name": "BQ storage alert",
				"owner": "someone@example.com", "lastAlerted": nil,
				"createTime": int64(1783503010786), "updateTime": int64(1783503080037),
			},
			columns: "name,owner,last alerted (UTC),updated (UTC)",
			cells:   map[string]interface{}{"last alerted (UTC)": "", "updated (UTC)": int64(1783503080037)},
		},
		{
			command:  "list-invoices",
			itemsKey: "invoices",
			row: map[string]interface{}{
				"id": "INV-US-26001403", "platform": "google-cloud", "status": "PAID",
				"invoiceDate": int64(1769817600000), "dueDate": int64(1772409600000),
				"totalAmount": 301022.7, "balanceAmount": int64(0), "currency": "USD",
				"url": "https://console.example.com/invoices/INV-US-26001403",
			},
			columns: "invoice,platform,issued,due,total,balance,status",
			linkURL: "url",
			cells: map[string]interface{}{
				"invoice": "INV-US-26001403", "issued": int64(1769817600000),
				"due": int64(1772409600000), "total": 301022.7, "balance": int64(0),
			},
		},
		{
			command:  "list-assets",
			itemsKey: "assets",
			row: map[string]interface{}{
				"id": "amazon-web-services-005097884916", "name": "partnerops-msp-dev",
				"type": "amazon-web-services", "createTime": int64(1776854665476),
				"url": "https://console.example.com/assets/amazon-web-services",
			},
			columns: "name,type,created (UTC)",
			linkURL: "url",
			cells:   map[string]interface{}{"created (UTC)": int64(1776854665476)},
		},
		{
			command:  "list-labels",
			itemsKey: "labels",
			row: map[string]interface{}{
				"id": "cEnk7VN9x7hibWcLIgqh", "name": "House ANA", "type": "custom",
				"color": "apricot", "createTime": "2026-07-06T11:45:06.445274Z",
				"updateTime": "2026-07-06T11:45:41.503007Z",
			},
			columns: "name,type,color,updated (UTC)",
			cells:   map[string]interface{}{"updated (UTC)": "2026-07-06T11:45:41.503007Z"},
		},
		{
			// The documented customer contract (TicketListItem).
			command:  "list-tickets",
			itemsKey: "tickets",
			row: map[string]interface{}{
				"id": int64(306123), "subject": "App Engine CreateVersion failures",
				"status": "closed", "severity": "high", "requester": "someone@example.com",
				"createTime": int64(1746718583000), "updateTime": int64(1748970150000),
				"urlUI": "https://console.example.com/support/tickets/306123",
			},
			columns: "subject,status,severity,updated (UTC)",
			linkURL: "urlUI",
			cells:   map[string]interface{}{"severity": "high", "updated (UTC)": int64(1748970150000)},
		},
		{
			// The raw Zendesk shape DoiT-employee (doer) sessions receive.
			command:  "list-tickets",
			itemsKey: "tickets",
			row: map[string]interface{}{
				"id": int64(306123), "subject": "App Engine CreateVersion failures",
				"status": "closed", "priority": "high",
				"created_at": "2026-05-08T15:36:23Z", "updated_at": "2026-06-03T17:02:30Z",
			},
			columns: "subject,status,severity,updated (UTC)",
			linkURL: "urlUI",
			cells:   map[string]interface{}{"severity": "high", "updated (UTC)": "2026-06-03T17:02:30Z"},
		},
		{
			command:  "list-users",
			itemsKey: "users",
			row: map[string]interface{}{
				"id": "hVOgqIg3NjSRQg8i0KJW", "email": "someone@example.com", "status": "Active",
				"lastLogin": "2026-03-10T16:16:50.888Z", "mfaEnrolled": nil, "roleId": "r1",
				"hasAccessKey": true, "userNotifications": []interface{}{int64(2), int64(3)},
			},
			columns: "email,role,status,last login (UTC),mfa enrolled",
			cells: map[string]interface{}{
				"role": "FinOps Admin", "last login (UTC)": "2026-03-10T16:16:50.888Z", "mfa enrolled": nil,
			},
			names: []nameCacheEntry{{ID: "r1", Name: "FinOps Admin"}},
		},
		{
			command:  "list-roles",
			itemsKey: "roles",
			row: map[string]interface{}{
				"id": "59w2TJPTCa3XPsJ3KITY", "name": "FinOps Admin", "type": "preset",
				"description": "Full analytics access", "permissions": []interface{}{"p1"},
			},
			columns: "name,type,description",
			cells:   map[string]interface{}{"name": "FinOps Admin"},
		},
		{
			command:  "list-annotations",
			itemsKey: "annotations",
			row: map[string]interface{}{
				"id": "g6rAMFoNN0MmAAZ4JsSK", "content": "deploy103.60.0",
				"labels":    []interface{}{map[string]interface{}{"id": "l1", "name": "repo:omni"}},
				"reports":   []interface{}{},
				"timestamp": "2026-08-18T16:49:18Z", "createTime": "2026-08-18T16:49:19.073488Z",
			},
			columns: "content,labels,reports,annotated (UTC)",
			cells:   map[string]interface{}{"labels": "repo:omni", "reports": "", "annotated (UTC)": "2026-08-18T16:49:18Z"},
		},
		{
			command:  "list-cloud-incidents",
			itemsKey: "incidents",
			row: map[string]interface{}{
				"id": "i1", "title": "GKE control plane degradation", "platform": "google-cloud",
				"product": "GKE", "status": "closed", "createTime": int64(1786924800000),
			},
			columns: "title,platform,product,status,created (UTC)",
			cells:   map[string]interface{}{"created (UTC)": int64(1786924800000)},
		},
		{
			command:  "list-commitments",
			itemsKey: "commitments",
			row: map[string]interface{}{
				"id": "PQqsrbE3x8dXeckBvD1f", "name": "2026 Contract", "cloudProvider": "google-cloud",
				"currency": "USD", "totalCommitmentValue": int64(4000000),
				"totalCurrentAttainment": 2673691.487, "totalForecastValue": int64(4355238),
				"startDate": "2026-01-01T00:00:00Z", "endDate": "2026-12-31T00:00:00Z",
				"updateTime": int64(1774901273698),
			},
			columns: "name,provider,commitment,attainment,forecast,start,end",
			cells: map[string]interface{}{
				"provider": "google-cloud", "commitment": "$4,000,000",
				"attainment": "$2,673,691", "start": "2026-01-01T00:00:00Z",
			},
		},
		{
			command:  "list-cloudflows",
			itemsKey: "items",
			row: map[string]interface{}{
				"id": "G2zdE9inbvwxBc38FCVc", "name": "Delete unused IPs", "published": true,
				"triggerType": "triggerNode", "lastExecutionStatus": "complete",
				"lastExecutedTime": "2026-08-14T13:10:29.705Z", "nextRun": "2026-08-16T13:00:00Z",
			},
			columns: "name,published,trigger,run status,last run (UTC),next run (UTC)",
			cells:   map[string]interface{}{"trigger": "triggerNode", "run status": "complete"},
		},
		{
			command:  "list-budget-suggestions",
			itemsKey: "items",
			row: map[string]interface{}{
				"id": "BYRp0IQy6hLM2fPlDAwj", "name": "AWS — OpenSearch",
				"amount":     map[string]interface{}{"amount": "3120", "currency": "USD"},
				"confidence": "high", "timeInterval": "month", "status": "pending",
			},
			columns: "name,amount,confidence,interval,status",
			cells:   map[string]interface{}{"amount": "$3,120", "interval": "month"},
		},
		{
			command:  "list-datahub-datasets",
			itemsKey: "datasets",
			row: map[string]interface{}{
				"name": "team-dataset", "lastUpdated": "2026-07-30T08:23:00Z", "updatedBy": "someone@example.com",
			},
			columns: "name,updated (UTC),updated by",
			cells:   map[string]interface{}{"updated (UTC)": "2026-07-30T08:23:00Z", "updated by": "someone@example.com"},
		},
	}
}

func TestListViewAnnotationsResolveReportNames(t *testing.T) {
	resetListViewTest(t, "list-annotations")
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		if listPath != reportsListPath {
			t.Fatalf("listPath = %q, want %q", listPath, reportsListPath)
		}
		return resolverListResult{entries: []nameCacheEntry{{ID: "RepA0000000000000000", Name: "Monthly AWS Spend"}}}, nil
	}
	body := map[string]interface{}{"annotations": []interface{}{
		map[string]interface{}{
			"content": "deploy", "labels": []interface{}{},
			"reports":   []interface{}{"RepA0000000000000000", "UnknownRep0000000000"},
			"timestamp": "2026-08-18T16:49:18Z",
		},
	}}
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["annotations"].([]interface{})[0].(map[string]interface{})
	if row["reports"] != "Monthly AWS Spend, UnknownRep0000000000" {
		t.Errorf("reports = %v, want resolved names with raw-id fallback", row["reports"])
	}
}

func TestListViewFoldersResolveParentNames(t *testing.T) {
	resetListViewTest(t, "list-folders")
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		return resolverListResult{entries: []nameCacheEntry{{ID: "ParentFolderId0000000", Name: "FinOps"}}}, nil
	}
	body := map[string]interface{}{"folders": []interface{}{
		map[string]interface{}{"name": "child", "parentFolderId": "ParentFolderId0000000", "description": ""},
		map[string]interface{}{"name": "top", "parentFolderId": "root", "description": ""},
	}}
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["folders"].([]interface{})
	if got := rows[0].(map[string]interface{})["parent folder"]; got != "FinOps" {
		t.Errorf("parent folder = %v, want the resolved name", got)
	}
	if got := rows[1].(map[string]interface{})["parent folder"]; got != "" {
		t.Errorf("parent folder = %v, want blank for root", got)
	}
}

func TestListViewsCurateTier1Commands(t *testing.T) {
	for _, tc := range listViewCases() {
		t.Run(tc.command, func(t *testing.T) {
			resetListViewTest(t, tc.command)
			if tc.names != nil {
				entries := tc.names
				resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
					return resolverListResult{entries: entries}, nil
				}
			}
			body := map[string]interface{}{tc.itemsKey: []interface{}{tc.row}}
			root := transformSuccessBody(body).(map[string]interface{})
			row := root[tc.itemsKey].([]interface{})[0].(map[string]interface{})

			for cell, want := range tc.cells {
				if got := row[cell]; got != want {
					t.Errorf("%s = %#v, want %#v", cell, got, want)
				}
			}
			if cols := viper.GetString("table-columns"); cols != tc.columns {
				t.Errorf("table-columns = %q, want %q", cols, tc.columns)
			}
			if !viper.GetBool("table-columns-auto") {
				t.Error("table-columns-auto = false, want true")
			}
			lead := tc.columns[:indexOrLen(tc.columns, ',')]
			if got := viper.GetString("table-priority-column"); got != lead {
				t.Errorf("table-priority-column = %q, want %q", got, lead)
			}
			wantLinkColumn := ""
			if tc.linkURL != "" {
				wantLinkColumn = lead
			}
			if got := viper.GetString("table-link-column"); got != wantLinkColumn {
				t.Errorf("table-link-column = %q, want %q", got, wantLinkColumn)
			}
			if got := viper.GetString("table-link-url-key"); got != tc.linkURL {
				t.Errorf("table-link-url-key = %q, want %q", got, tc.linkURL)
			}
		})
	}
}

func indexOrLen(s string, sep byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return i
		}
	}
	return len(s)
}

func TestListViewsMachineFormatsKeepRawFields(t *testing.T) {
	for _, tc := range listViewCases() {
		t.Run(tc.command, func(t *testing.T) {
			resetListViewTest(t, tc.command)
			viper.Set("rsh-output-format", "json")
			body := map[string]interface{}{tc.itemsKey: []interface{}{tc.row}}
			before := len(tc.row)
			transformSuccessBody(body)
			if len(tc.row) != before {
				t.Errorf("row grew from %d to %d fields, want raw response untouched for machine formats", before, len(tc.row))
			}
			if cols := viper.GetString("table-columns"); cols != "" {
				t.Errorf("table-columns = %q, want unset for machine formats", cols)
			}
		})
	}
}

func TestListViewAlertsSortsByUpdatedDescending(t *testing.T) {
	resetListViewTest(t, "list-alerts")
	body := map[string]interface{}{"alerts": []interface{}{
		map[string]interface{}{"name": "stale", "updateTime": int64(1780000000000)},
		map[string]interface{}{"name": "fresh", "updateTime": int64(1787000000000)},
	}}
	root := transformSuccessBody(body).(map[string]interface{})
	rows := root["alerts"].([]interface{})
	if got := rows[0].(map[string]interface{})["name"]; got != "fresh" {
		t.Errorf("first alert = %v, want fresh (sorted by updateTime desc)", got)
	}
}

func TestListViewAllocationsResolveFolders(t *testing.T) {
	resetListViewTest(t, "list-allocations")
	resolverListFetch = func(listPath, context string, maxPages int) (resolverListResult, error) {
		if listPath != foldersListPath {
			t.Fatalf("listPath = %q, want %q", listPath, foldersListPath)
		}
		return resolverListResult{entries: []nameCacheEntry{{ID: "T0bkYjXi5fOfFNiF5Zhf", Name: "House ANA"}}}, nil
	}
	body := map[string]interface{}{"allocations": []interface{}{
		map[string]interface{}{"name": "a", "folderId": "T0bkYjXi5fOfFNiF5Zhf", "updateTime": int64(1)},
	}}
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["allocations"].([]interface{})[0].(map[string]interface{})
	if row["folder"] != "House ANA" {
		t.Errorf("folder = %v, want the resolved folder name", row["folder"])
	}
}

// Zendesk tickets responses sideload a `users` array that can be larger than
// `tickets`; the view must point the table renderer at the right one.
func TestListViewTicketsPicksTicketsOverSideloadedUsers(t *testing.T) {
	resetListViewTest(t, "list-tickets")
	ticket := map[string]interface{}{"subject": "s", "status": "open", "priority": "high", "updated_at": "2026-06-03T17:02:30Z"}
	body := map[string]interface{}{
		"tickets": []interface{}{ticket},
		"users":   []interface{}{map[string]interface{}{"id": 1}, map[string]interface{}{"id": 2}},
	}
	root := transformSuccessBody(body).(map[string]interface{})
	picked := pickObjectArrayField(root).([]interface{})
	if len(picked) != 1 {
		t.Fatalf("picked array len = %d, want 1 (the tickets array, not sideloaded users)", len(picked))
	}
	if picked[0].(map[string]interface{})["subject"] != "s" {
		t.Error("picked array is not the tickets array")
	}
}

// The curated view (and its row shaping) deliberately does not apply under an
// explicit -C/--fields selection or a machine format like csv — but the row
// picker must still choose the command's primary collection over a larger
// sideloaded array.
func TestTicketsRowsPreferPrimaryCollectionWithoutCuratedView(t *testing.T) {
	ticketsAndUsers := func() map[string]interface{} {
		return map[string]interface{}{
			"tickets": []interface{}{
				map[string]interface{}{"subject": "s1", "status": "open"},
				map[string]interface{}{"subject": "s2", "status": "open"},
				map[string]interface{}{"subject": "s3", "status": "closed"},
			},
			"users": []interface{}{
				map[string]interface{}{"id": int64(372376685811)},
				map[string]interface{}{"id": int64(372376685812)},
				map[string]interface{}{"id": int64(372376685813)},
				map[string]interface{}{"id": int64(372376685814)},
			},
		}
	}
	for name, configure := range map[string]func(){
		"explicit column selection": func() { viper.Set("table-columns", "subject,status") },
		"csv output":                func() { viper.Set("rsh-output-format", "csv") },
	} {
		t.Run(name, func(t *testing.T) {
			resetListViewTest(t, "list-tickets")
			configure()
			root := transformSuccessBody(ticketsAndUsers()).(map[string]interface{})
			rows, err := toTableRows(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 3 {
				t.Fatalf("row count = %d, want 3 tickets (got the sideloaded users array?)", len(rows))
			}
			if rows[0]["subject"] != "s1" {
				t.Errorf("rows[0] = %#v, want the first ticket", rows[0])
			}
		})
	}
}

func TestListViewInvoiceCreditMemoBlanksZeroTimeDueDate(t *testing.T) {
	resetListViewTest(t, "list-invoices")
	body := map[string]interface{}{"invoices": []interface{}{
		map[string]interface{}{
			"id": "CM-US-26000060", "platform": "google-cloud", "status": "PAID",
			"invoiceDate": int64(1769817600000), "dueDate": int64(-62135596800000),
			"totalAmount": -301022.7, "balanceAmount": int64(0), "currency": "USD",
		},
	}}
	root := transformSuccessBody(body).(map[string]interface{})
	row := root["invoices"].([]interface{})[0].(map[string]interface{})
	if row["due"] != "" {
		t.Errorf("due = %#v, want blank for the Go-zero-time sentinel", row["due"])
	}
}

func TestMoneyNamedColumnCoversInvoiceFields(t *testing.T) {
	for name, want := range map[string]bool{
		"total": true, "balance": true, "amount": true, "spend to date": true,
		"anomaly cost": true, "subject": false, "risk": false,
	} {
		if got := moneyNamedColumn(name); got != want {
			t.Errorf("moneyNamedColumn(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestDisplayTimeColumnTitleRewrite(t *testing.T) {
	// UTC context (unresolved zone): titles keep their canonical suffix.
	prev := displayTimeLocation
	displayTimeLocation = nil
	t.Cleanup(func() { displayTimeLocation = prev })
	if got := displayTimeColumnTitle("updated (UTC)", "updateTime"); got != "updated (UTC)" {
		t.Errorf("UTC context title = %q, want unchanged", got)
	}

	// Localized context: instant columns advertise local rendering...
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	displayTimeLocation = loc
	viper.Set("rsh-output-format", "table")
	t.Cleanup(func() { viper.Set("rsh-output-format", nil) })
	if got := displayTimeColumnTitle("updated (UTC)", "updateTime"); got != "updated (local)" {
		t.Errorf("localized title = %q, want updated (local)", got)
	}
	if got := displayTimeColumnTitle("owner", ""); got != "owner" {
		t.Errorf("non-time title = %q, want unchanged", got)
	}

	// ...but UTC-label columns (anomaly windows) keep title and zone.
	viper.Set("utc-label-columns", "startTime,endTime,started (UTC)")
	t.Cleanup(func() { viper.Set("utc-label-columns", "") })
	if got := displayTimeColumnTitle("started (UTC)", "startTime"); got != "started (UTC)" {
		t.Errorf("UTC-label title = %q, want kept as (UTC)", got)
	}
}

func TestAnomalyCuratedViewStaysUTCUnderZone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	prevLoc := displayTimeLocation
	displayTimeLocation = loc
	prevCmd := invokedCommandName
	invokedCommandName = "list-anomalies"
	viper.Set("rsh-output-format", "table")
	viper.Set("table-columns", "")
	viper.Set("raw-numbers", false)
	t.Cleanup(func() {
		displayTimeLocation = prevLoc
		invokedCommandName = prevCmd
		viper.Set("rsh-output-format", nil)
		viper.Set("table-columns", nil)
		viper.Set("raw-numbers", nil)
		viper.Set("utc-label-columns", "")
	})

	hourlyStart := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC).UnixMilli()
	body := map[string]interface{}{
		"anomalies": []interface{}{map[string]interface{}{
			"serviceName": "S3", "severityLevel": "warning", "costOfAnomaly": 10.0,
			"platform": "aws", "status": "active", "startTime": hourlyStart,
			"acknowledgedAt": "2026-08-12T09:15:30Z",
		}},
		"rowCount": int64(1),
	}
	transformSuccessBody(body)
	row := body["anomalies"].([]interface{})[0].(map[string]interface{})

	// The curated column keeps its (UTC) title, and the renderer keeps the
	// value in UTC ("01:00", not "04:00" IDT)...
	if !strings.Contains(viper.GetString("table-columns"), "started (UTC)") {
		t.Fatalf("curated columns = %q, want started (UTC)", viper.GetString("table-columns"))
	}
	if got := tableCellText("started (UTC)", row["started (UTC)"]); got != "2026-08-12 01:00" {
		t.Errorf("started (UTC) cell = %q, want UTC label", got)
	}
	// ...while sibling instants on the same row localize.
	if got := tableCellText("acknowledgedAt", row["acknowledgedAt"]); got != "2026-08-12 12:15" {
		t.Errorf("acknowledgedAt cell = %q, want localized instant", got)
	}
}
