package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/steven509/testdash/internal/catalog"
	"github.com/steven509/testdash/internal/databases"
	"github.com/steven509/testdash/internal/jsonx"
	"github.com/steven509/testdash/internal/kafka"
	"github.com/steven509/testdash/internal/logformat"
	"github.com/steven509/testdash/internal/progress"
	"github.com/steven509/testdash/internal/services"
	"github.com/steven509/testdash/internal/settings"
	"github.com/steven509/testdash/internal/wiremock"
)

func TestDemoScreens(t *testing.T) {
	out := os.Getenv("TESTDASH_DEMO")
	if out == "" {
		t.Skip("set TESTDASH_DEMO to a directory to write the demo screens")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prefs := settings.Defaults()
	prefs.NerdFont = os.Getenv("DEMO_NERD") != ""
	m := New(ctx, Options{Workdir: "/work/orders", RunDir: t.TempDir()}, prefs)
	m.catalog = &catalog.Catalog{Paths: []string{":app:test"}, Tasks: map[string]*catalog.TestTask{":app:test": {Path: ":app:test", Entries: []*catalog.Entry{
		entry("c1", "", "class", "com.shop.orders.OrderApiTest", "", "OrderApiTest"),
		entry("t1", "c1", "test", "com.shop.orders.OrderApiTest", "createsOrder", "creates an order and publishes it"),
		entry("t2", "c1", "test", "com.shop.orders.OrderApiTest", "rejectsUnknownCustomer", "rejects an unknown customer"),
		entry("t3", "c1", "test", "com.shop.orders.OrderApiTest", "listsOrders", "lists a customer's orders"),
		entry("c2", "", "class", "com.shop.orders.PaymentTest", "", "PaymentTest"),
		entry("t4", "c2", "test", "com.shop.orders.PaymentTest", "chargesCard", "charges the card"),
		entry("t5", "c2", "test", "com.shop.orders.PaymentTest", "refunds", "refunds a cancelled order"),
	}}}}
	m.opts.BuildRoot = "/work/orders"
	m.chooseTask(":app:test", true)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 42})

	base := time.Now().Add(-10 * time.Second)
	at := func(ms int) int64 { return base.Add(time.Duration(ms) * time.Millisecond).UnixMilli() }
	m.runStarted, m.runCommand = base.Add(-3*time.Second), "/work/orders/gradlew :app:test --tests com.shop.orders.OrderApiTest --tests com.shop.orders.OrderApiTest$* --tests com.shop.orders.PaymentTest --tests com.shop.orders.PaymentTest$*"
	m.progress = progress.New(m.opts.RunDir)
	m.inRun = map[string]bool{}
	events := fmt.Sprintf(`{"event":"plan","at":1,"entries":[{"id":"c1","kind":"class","className":"com.shop.orders.OrderApiTest","name":"OrderApiTest"},{"id":"t1","parent":"c1","kind":"test","className":"com.shop.orders.OrderApiTest","methodName":"createsOrder","name":"creates an order and publishes it"},{"id":"t2","parent":"c1","kind":"test","className":"com.shop.orders.OrderApiTest","methodName":"rejectsUnknownCustomer","name":"rejects an unknown customer"},{"id":"t3","parent":"c1","kind":"test","className":"com.shop.orders.OrderApiTest","methodName":"listsOrders","name":"lists a customer's orders"},{"id":"c2","kind":"class","className":"com.shop.orders.PaymentTest","name":"PaymentTest"},{"id":"t4","parent":"c2","kind":"test","className":"com.shop.orders.PaymentTest","methodName":"chargesCard","name":"charges the card"},{"id":"t5","parent":"c2","kind":"test","className":"com.shop.orders.PaymentTest","methodName":"refunds","name":"refunds a cancelled order"}]}
{"event":"started","at":%[8]d,"id":"c1"}
{"event":"started","at":%[1]d,"id":"t1"}
{"event":"finished","at":%[2]d,"id":"t1","status":"FAILED","message":"AssertionFailedError: expected: <201> but was: <502>","trace":"org.opentest4j.AssertionFailedError: expected: <201> but was: <502>\n\tat org.junit.jupiter.api.AssertionFailureBuilder.build(AssertionFailureBuilder.java:151)\n\tat org.junit.jupiter.api.AssertEquals.failNotEqual(AssertEquals.java:197)\n\tat org.junit.jupiter.api.Assertions.assertEquals(Assertions.java:563)\n\tat com.shop.orders.OrderApiTest.createsOrder(OrderApiTest.java:48)\n\tat java.base/java.lang.reflect.Method.invoke(Method.java:580)\n\tat org.junit.platform.commons.util.ReflectionUtils.invokeMethod(ReflectionUtils.java:728)"}
{"event":"started","at":%[3]d,"id":"t2"}
{"event":"finished","at":%[4]d,"id":"t2","status":"SUCCESSFUL"}
{"event":"started","at":%[5]d,"id":"t3"}
{"event":"finished","at":%[6]d,"id":"t3","status":"SUCCESSFUL"}
{"event":"finished","at":%[6]d,"id":"c1","status":"FAILED"}
{"event":"started","at":%[6]d,"id":"c2"}
{"event":"started","at":%[7]d,"id":"t4"}
`, at(0), at(1400), at(1500), at(1900), at(2000), at(2300), at(4400), at(-2500))
	if err := os.WriteFile(filepath.Join(m.opts.RunDir, "progress-1.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	m.runFinished = time.Time{}
	m.Update(tickMsg{})

	app := services.NewService("orders-api", "app", "shop/orders-api:latest")
	m.Update(services.Added{Service: app})
	wm := services.NewService("wiremock", "wiremock", "wiremock/wiremock:3.9.1")
	m.Update(services.Added{Service: wm})
	kf := services.NewService("kafka", "kafka", "confluentinc/cp-kafka:7.6.0")
	m.Update(services.Added{Service: kf})
	db := services.NewService("mariadb", "mariadb", "mariadb:11.8")
	m.Update(services.Added{Service: db})
	line := func(ms int, text string) *logformat.Line { return logformat.NewLine(time.UnixMilli(at(ms)), text) }
	m.Update(services.Lines{Service: app, Gen: m.gen, Lines: []*logformat.Line{
		line(100, "2026-10-12 15:45:51.006  INFO 1 --- [orders] [nio-8080-exec-1] c.s.o.OrderController : [customer=42] creating order"),
		line(450, "2026-10-12 15:45:51.406  WARN 1 --- [orders] [nio-8080-exec-1] c.s.o.PaymentClient   : [customer=42] payment service slow to answer (1200 ms)"),
		line(1300, "2026-10-12 15:45:52.306 ERROR 1 --- [orders] [nio-8080-exec-1] c.s.o.OrderController : [customer=42] could not create order"),
		line(1301, "org.springframework.web.client.HttpServerErrorException$BadGateway: 502 Bad Gateway"),
		line(1302, "\tat com.shop.orders.PaymentClient.charge(PaymentClient.java:61)"),
		line(1303, "\tat com.shop.orders.OrderService.create(OrderService.java:33)"),
		line(1600, "2026-10-12 15:45:52.606  INFO 1 --- [orders] [nio-8080-exec-2] c.s.o.OrderController : listing orders"),
	}})
	request := func(id string, ms int, method, url string, status int, matched bool) wiremock.ServeEvent {
		raw := fmt.Sprintf(`{"id":%q,"request":{"url":%q,"absoluteUrl":"http://wiremock:8080%s","method":%q,"loggedDate":%d,"headers":{"Content-Type":"application/json"},"body":"{\"amount\":1999,\"currency\":\"GBP\"}"},"response":{"status":%d,"body":"{\"error\":\"upstream timeout\"}"},"wasMatched":%v}`,
			id, url, url, method, at(ms), status, matched)
		object, _ := jsonx.ParseObject([]byte(raw))
		return wiremock.ServeEvent{ID: id, At: time.UnixMilli(at(ms)), Method: method, URL: url, Status: status, Matched: matched, Raw: object}
	}
	wmView := m.serviceEntry(wm).view.(*wiremockView)
	m.Update(wiremockMsg{view: wmView, poll: wiremockPoll{hasEvents: true, events: []wiremock.ServeEvent{
		request("e1", 200, "GET", "/customers/42", 200, true),
		request("e2", 500, "POST", "/payments", 502, true),
		request("e3", 1700, "GET", "/customers/7/orders?page=1", 404, false),
	}}})
	key, value := "order-1001", `{"orderId":1001,"customer":42,"status":"PENDING"}`
	trace, typeID, source := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "com.shop.orders.OrderCreated",
		`{"service":"orders-api","version":"1.4.2"}`
	m.Update(kafkaMessagesMsg{view: m.serviceEntry(kf).view.(*kafkaView), batch: []kafka.Message{
		{At: time.UnixMilli(at(300)), Topic: "orders.created", Partition: 0, Offset: 17, Key: &key, Value: &value,
			Headers: []kafka.Header{{Name: "traceparent", Value: &trace}, {Name: "__TypeId__", Value: &typeID},
				{Name: "x-retry", Value: nil}, {Name: "source", Value: &source}}},
	}})
	m.Update(queriesMsg{view: m.serviceEntry(db).view.(*dbView), batch: []databases.Query{
		{At: time.UnixMilli(at(150)), Connection: "12", Kind: "Query", Text: "select id, name, credit_limit from customers where id = 42"},
		{At: time.UnixMilli(at(260)), Connection: "12", Kind: "Execute", Text: "insert into orders (customer_id, total, status) values (42, 1999, 'PENDING')"},
	}})

	write := func(name string) {
		m.dirty = true
		if err := os.WriteFile(filepath.Join(out, name+".txt"), []byte(m.View().Content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 52})
	write("dashboard")
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	m.tree.cursor = "t1"
	m.version++
	write("test")
	m.setFocus(m.inspector.events)
	m.inspector.events.moveCursor(5)
	m.View()
	write("timeline")
	m.serviceList.selectEntry(m, m.serviceEntry(wm))
	m.setFocus(m.serviceList)
	write("wiremock")
	m.serviceList.selectEntry(m, m.serviceEntry(kf))
	kafkaView := m.serviceEntry(kf).view.(*kafkaView)
	m.setFocus(kafkaView.table)
	write("kafka-value")
	press(m, "h")
	write("kafka-headers")
	m.popup = &confirmQuit{}
	write("quit")
	m.popup = nil
	m.popup = &help{}
	write("help")
	m.popup = &settingsOverlay{form: newSettingsForm(m.prefs, m.applySettings)}
	write("settings")
	m.popup = nil
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m.setFocus(m.tree)
	m.tree.cursor = "t1"
	write("narrow-lists")
	m.setFocus(m.inspector.events)
	write("narrow-details")
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	useIcons(true, 1)
	m.setFocus(m.tree)
	write("nerd")
	useIcons(false, 1)
	settingsApp := NewSettingsApp(settings.Defaults())
	settingsApp.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	if err := os.WriteFile(filepath.Join(out, "settings-app.txt"), []byte(settingsApp.View().Content), 0o644); err != nil {
		t.Fatal(err)
	}
}
