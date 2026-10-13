package com.example.shop.events;

import com.example.shop.logging.Log;
import com.fasterxml.jackson.databind.node.ObjectNode;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.Properties;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import org.apache.kafka.clients.producer.KafkaProducer;
import org.apache.kafka.clients.producer.ProducerConfig;
import org.apache.kafka.clients.producer.ProducerRecord;
import org.apache.kafka.clients.producer.RecordMetadata;
import org.apache.kafka.common.serialization.StringSerializer;

/** Publishes the shop's domain events to Kafka, with the headers other services route and trace by. */
public final class EventPublisher implements AutoCloseable {
    private static final Log log = Log.get(EventPublisher.class);

    private final KafkaProducer<String, String> producer;

    public EventPublisher(String bootstrapServers) {
        Properties config = new Properties();
        config.put(ProducerConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrapServers);
        config.put(ProducerConfig.CLIENT_ID_CONFIG, "shop");
        config.put(ProducerConfig.ACKS_CONFIG, "all");
        config.put(ProducerConfig.LINGER_MS_CONFIG, 2);
        config.put(ProducerConfig.KEY_SERIALIZER_CLASS_CONFIG, StringSerializer.class);
        config.put(ProducerConfig.VALUE_SERIALIZER_CLASS_CONFIG, StringSerializer.class);
        producer = new KafkaProducer<>(config);
    }

    public void publish(String topic, String key, String type, String traceId, ObjectNode event) {
        event.put("type", type).put("occurredAt", Instant.now().toString());
        ProducerRecord<String, String> record = new ProducerRecord<>(topic, key, event.toString());
        record.headers()
                .add("eventType", bytes(type))
                .add("traceId", bytes(traceId))
                .add("source", bytes("shop"))
                .add("schemaVersion", bytes("3"))
                .add("contentType", bytes("application/json"));
        String traceparent = Log.traceparent();
        if (traceparent != null) {
            record.headers().add("traceparent", bytes(traceparent));
        }
        try {
            RecordMetadata sent = producer.send(record).get(10, TimeUnit.SECONDS);
            log.info("Published %s to %s-%d at offset %d", type, topic, sent.partition(), sent.offset());
        } catch (ExecutionException | TimeoutException e) {
            throw new IllegalStateException("Could not publish " + type + " to " + topic, e);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IllegalStateException(e);
        }
    }

    @Override
    public void close() {
        producer.close();
    }

    private static byte[] bytes(String text) {
        return text.getBytes(StandardCharsets.UTF_8);
    }
}
