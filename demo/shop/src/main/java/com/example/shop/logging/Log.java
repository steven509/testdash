package com.example.shop.logging;

import java.time.OffsetDateTime;
import java.time.format.DateTimeFormatter;
import java.util.HexFormat;
import java.util.concurrent.ThreadLocalRandom;

/** Writes lines in Spring Boot's console format, so the shop's log reads like any Spring service's. */
public final class Log {
    private static final DateTimeFormatter TIME = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:mm:ss.SSSXXX");
    private static final long PID = ProcessHandle.current().pid();
    private static final ThreadLocal<String> CORRELATION = new ThreadLocal<>();
    private static final ThreadLocal<String> CONTEXT = new ThreadLocal<>();

    private final String name;

    private Log(String name) {
        this.name = abbreviate(name, 40);
    }

    public static Log get(Class<?> type) {
        return new Log(type.getName());
    }

    static Log get(String name) {
        return new Log(name);
    }

    /** Starts a request's trace, returning its id. */
    public static String startTrace() {
        String traceId = randomHex(16);
        CORRELATION.set(traceId + "-" + randomHex(8));
        return traceId;
    }

    public static void endTrace() {
        CORRELATION.remove();
        CONTEXT.remove();
    }

    /** Puts a key and value before every message, as an MDC entry in a Spring Boot pattern would. */
    public static void context(String key, Object value) {
        CONTEXT.set(key + "=" + value);
    }

    public void info(String message, Object... args) {
        write("INFO", message.formatted(args), null);
    }

    public void warn(String message, Object... args) {
        write("WARN", message.formatted(args), null);
    }

    public void error(String message, Throwable error) {
        write("ERROR", message, error);
    }

    void write(String level, String message, Throwable error) {
        StringBuilder line = new StringBuilder()
                .append(OffsetDateTime.now().format(TIME))
                .append(' ').append("%5s".formatted(level))
                .append(' ').append(PID).append(" --- [shop] [")
                .append("%15s".formatted(tail(Thread.currentThread().getName(), 15))).append("] ");
        String correlation = CORRELATION.get();
        if (correlation != null) {
            line.append('[').append(correlation).append("] ");
        }
        line.append("%-40s".formatted(name)).append(" : ");
        String context = CONTEXT.get();
        if (context != null) {
            line.append('[').append(context).append("] ");
        }
        line.append(message);
        synchronized (System.out) {
            System.out.println(line);
            if (error != null) {
                error.printStackTrace(System.out);
            }
        }
    }

    private static String abbreviate(String name, int width) {
        String[] parts = name.split("\\.");
        for (int i = 0; i < parts.length - 1 && String.join(".", parts).length() > width; i++) {
            parts[i] = parts[i].substring(0, 1);
        }
        return String.join(".", parts);
    }

    private static String tail(String text, int width) {
        return text.length() <= width ? text : text.substring(text.length() - width);
    }

    private static String randomHex(int bytes) {
        byte[] random = new byte[bytes];
        ThreadLocalRandom.current().nextBytes(random);
        return HexFormat.of().formatHex(random);
    }
}
