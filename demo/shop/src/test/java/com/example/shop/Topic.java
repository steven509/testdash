package com.example.shop;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Properties;
import java.util.UUID;
import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.clients.consumer.KafkaConsumer;
import org.apache.kafka.common.header.Header;
import org.apache.kafka.common.serialization.StringDeserializer;

/** Reads a Kafka topic from the start, to find the event a test caused. */
final class Topic implements AutoCloseable {
    private final KafkaConsumer<String, String> consumer;

    Topic(String name, String group) {
        Properties config = new Properties();
        config.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, "localhost:19092");
        config.put(ConsumerConfig.GROUP_ID_CONFIG, group + "-" + UUID.randomUUID().toString().substring(0, 8));
        config.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        config.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class);
        config.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class);
        consumer = new KafkaConsumer<>(config);
        consumer.subscribe(List.of(name));
    }

    ConsumerRecord<String, String> await(String key, Duration timeout) {
        Instant deadline = Instant.now().plus(timeout);
        while (Instant.now().isBefore(deadline)) {
            for (ConsumerRecord<String, String> record : consumer.poll(Duration.ofMillis(100))) {
                if (key.equals(record.key())) {
                    return record;
                }
            }
        }
        throw new AssertionError("No event with key " + key + " within " + timeout.toSeconds() + "s");
    }

    static String header(ConsumerRecord<String, String> record, String name) {
        Header header = record.headers().lastHeader(name);
        return header == null ? null : new String(header.value(), StandardCharsets.UTF_8);
    }

    @Override
    public void close() {
        consumer.close();
    }
}
