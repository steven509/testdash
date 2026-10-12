package testdash;

import java.io.PrintStream;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;

import org.apache.kafka.clients.consumer.ConsumerRecord;
import org.apache.kafka.common.MessageFormatter;
import org.apache.kafka.common.header.Header;

/**
 * Prints each record kafka-console-consumer reads as one JSON line, with its topic, which the default formatter
 * cannot print, so one consumer can follow every topic.
 */
public class TestdashFormatter implements MessageFormatter {

    @Override
    public void writeTo(ConsumerRecord<byte[], byte[]> record, PrintStream output) {
        StringBuilder json = new StringBuilder(256)
                .append("{\"topic\":").append(quote(record.topic()))
                .append(",\"partition\":").append(record.partition())
                .append(",\"offset\":").append(record.offset())
                .append(",\"timestamp\":").append(record.timestamp())
                .append(",\"key\":").append(text(record.key()))
                .append(",\"headers\":{");
        boolean first = true;
        for (Header header : record.headers()) {
            json.append(first ? "" : ",").append(quote(header.key())).append(':').append(text(header.value()));
            first = false;
        }
        json.append("},\"value\":").append(text(record.value())).append('}');
        output.println(json);
        output.flush();
    }

    private static String text(byte[] bytes) {
        if (bytes == null) {
            return "null";
        }
        try {
            return quote(StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(bytes))
                    .toString());
        } catch (CharacterCodingException e) {
            return quote("<" + bytes.length + " bytes of binary>");
        }
    }

    private static String quote(String value) {
        StringBuilder quoted = new StringBuilder(value.length() + 2).append('"');
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            switch (c) {
                case '"': quoted.append("\\\""); break;
                case '\\': quoted.append("\\\\"); break;
                case '\n': quoted.append("\\n"); break;
                case '\r': quoted.append("\\r"); break;
                case '\t': quoted.append("\\t"); break;
                default:
                    if (c < 0x20) {
                        quoted.append(String.format("\\u%04x", (int) c));
                    } else {
                        quoted.append(c);
                    }
            }
        }
        return quoted.append('"').toString();
    }
}
