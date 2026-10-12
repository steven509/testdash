package kafka

import "testing"

func TestBootstrap(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{
			"KAFKA_LISTENERS":                      "PLAINTEXT://0.0.0.0:9092,BROKER://0.0.0.0:9093,CONTROLLER://0.0.0.0:9094",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "PLAINTEXT:PLAINTEXT,BROKER:PLAINTEXT,CONTROLLER:PLAINTEXT",
			"KAFKA_INTER_BROKER_LISTENER_NAME":     "BROKER",
			"KAFKA_CONTROLLER_LISTENER_NAMES":      "CONTROLLER",
		}, "localhost:9093"},
		{map[string]string{
			"KAFKA_CFG_LISTENERS":                      "SASL://:9092,PLAINTEXT://:29092",
			"KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP": "SASL:SASL_PLAINTEXT,PLAINTEXT:PLAINTEXT",
		}, "localhost:29092"},
		{map[string]string{}, "localhost:9092"},
	} {
		if got := Bootstrap(c.env); got != c.want {
			t.Errorf("Bootstrap(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestParseMessage(t *testing.T) {
	message, ok := parseMessage([]byte(`{"topic":"orders","partition":1,"offset":7,"timestamp":1760283951006,` +
		`"key":"k","headers":{"b":"2","a":null},"value":"{\"id\":1}"}`))
	if !ok {
		t.Fatal("not parsed")
	}
	if message.Topic != "orders" || message.Partition != 1 || message.Offset != 7 || *message.Key != "k" {
		t.Errorf("%+v", message)
	}
	if len(message.Headers) != 2 || message.Headers[0].Name != "b" || message.Headers[1].Value != nil {
		t.Errorf("headers keep their order and nulls: %+v", message.Headers)
	}
	if _, ok := parseMessage([]byte("[2026-10-12] WARN consumer chatter")); ok {
		t.Error("the consumer's own lines are not messages")
	}
}
