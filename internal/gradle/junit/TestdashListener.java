package testdash;

import java.io.IOException;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.io.Writer;
import java.lang.reflect.Field;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;

import org.junit.platform.engine.TestExecutionResult;
import org.junit.platform.engine.TestSource;
import org.junit.platform.engine.support.descriptor.ClassSource;
import org.junit.platform.engine.support.descriptor.MethodSource;
import org.junit.platform.launcher.TestExecutionListener;
import org.junit.platform.launcher.TestIdentifier;
import org.junit.platform.launcher.TestPlan;

/**
 * Writes a JUnit Platform run's events for testdash, one JSON object per line, to
 * progress-[pid].jsonl in the directory the testdash.dir system property names. Does nothing without it.
 *
 * The plan event lists every class, test and test template in run order before anything runs, so the
 * dashboard knows what is left; tests a template (such as a parameterised test) creates follow as
 * registered events. A failure to write disables the listener rather than failing the run.
 *
 * Entries carry the fully qualified class name and the method name, which are what a Gradle --tests filter
 * matches. Uses only API present since JUnit Platform 1.0, so one build works with any 1.x launcher.
 *
 * When Testcontainers is on the classpath, containers events name the labels it will clean up by: its session
 * id, and each Compose project's name, registered before the project starts. They tell this run's containers
 * from another run's.
 */
public class TestdashListener implements TestExecutionListener {

    private static final String RESOURCE_REAPER = "org.testcontainers.utility.ResourceReaper";

    private Writer out;
    private Thread containerWatcher;
    private final Set<String> containerLabels = new LinkedHashSet<>();

    @Override
    public synchronized void testPlanExecutionStarted(TestPlan testPlan) {
        String dir = System.getProperty("testdash.dir");
        if (dir == null) {
            return;
        }
        try {
            Path file = Paths.get(dir, "progress-" + ProcessHandle.current().pid() + ".jsonl");
            Files.createDirectories(file.getParent());
            out = Files.newBufferedWriter(file, StandardCharsets.UTF_8);
        } catch (IOException | RuntimeException e) {
            out = null;
            return;
        }
        write("plan", "\"entries\":[" + String.join(",", planEntries(testPlan)) + "]");
        watchContainers();
    }

    private void watchContainers() {
        Field deathNote;
        try {
            Class<?> reaper = Class.forName(RESOURCE_REAPER, true, getClass().getClassLoader());
            deathNote = reaper.getDeclaredField("DEATH_NOTE");
            deathNote.setAccessible(true);
        } catch (ReflectiveOperationException | RuntimeException | LinkageError e) {
            write("containers", "\"testcontainers\":false");
            return;
        }
        write("containers", "\"testcontainers\":true,\"labels\":[]");
        containerWatcher = new Thread(() -> {
            while (!Thread.currentThread().isInterrupted()) {
                reportContainerLabels(deathNote);
                try {
                    Thread.sleep(500);
                } catch (InterruptedException e) {
                    return;
                }
            }
        }, "testdash-containers");
        containerWatcher.setDaemon(true);
        containerWatcher.start();
    }

    /** ResourceReaper.DEATH_NOTE holds every label filter Testcontainers will clean up by, as ("label", "k=v"). */
    private void reportContainerLabels(Field deathNote) {
        List<String> labels = new ArrayList<>();
        try {
            Object filters = deathNote.get(null);
            synchronized (filters) {
                for (Object filter : (Iterable<?>) filters) {
                    for (Object entry : (Iterable<?>) filter) {
                        Map.Entry<?, ?> pair = (Map.Entry<?, ?>) entry;
                        if ("label".equals(pair.getKey())) {
                            labels.add(String.valueOf(pair.getValue()));
                        }
                    }
                }
            }
        } catch (ReflectiveOperationException | RuntimeException e) {
            return;
        }
        synchronized (this) {
            if (containerLabels.addAll(labels)) {
                List<String> quoted = new ArrayList<>();
                for (String label : containerLabels) {
                    quoted.add(json(label));
                }
                write("containers", "\"testcontainers\":true,\"labels\":[" + String.join(",", quoted) + "]");
            }
        }
    }

    /** Every class, test and test template of the plan as JSON entries, in run order. */
    static List<String> planEntries(TestPlan testPlan) {
        List<String> entries = new ArrayList<>();
        for (TestIdentifier engine : testPlan.getRoots()) {
            for (TestIdentifier child : testPlan.getChildren(engine)) {
                addInRunOrder(testPlan, child, entries);
            }
        }
        return entries;
    }

    private static void addInRunOrder(TestPlan testPlan, TestIdentifier identifier, List<String> entries) {
        String entry = entry(identifier);
        if (entry != null) {
            entries.add(entry);
        }
        for (TestIdentifier child : testPlan.getChildren(identifier)) {
            addInRunOrder(testPlan, child, entries);
        }
    }

    @Override
    public synchronized void dynamicTestRegistered(TestIdentifier testIdentifier) {
        String entry = entry(testIdentifier);
        if (entry != null) {
            write("registered", "\"entry\":" + entry);
        }
    }

    @Override
    public synchronized void executionStarted(TestIdentifier testIdentifier) {
        if (kind(testIdentifier) != null) {
            write("started", "\"id\":" + json(testIdentifier.getUniqueId()));
        }
    }

    @Override
    public synchronized void executionSkipped(TestIdentifier testIdentifier, String reason) {
        if (kind(testIdentifier) != null) {
            write("skipped", "\"id\":" + json(testIdentifier.getUniqueId()) + ",\"message\":" + json(reason));
        }
    }

    @Override
    public synchronized void executionFinished(TestIdentifier testIdentifier, TestExecutionResult result) {
        if (kind(testIdentifier) == null) {
            return;
        }
        String message = result.getThrowable()
            .map(t -> t.getClass().getSimpleName() + ": " + t.getMessage())
            .map(m -> m.length() > 2000 ? m.substring(0, 2000) : m)
            .orElse(null);
        String trace = result.getThrowable().map(TestdashListener::trace).orElse(null);
        write("finished", "\"id\":" + json(testIdentifier.getUniqueId())
            + ",\"status\":" + json(result.getStatus().name())
            + ",\"message\":" + json(message)
            + ",\"trace\":" + json(trace));
    }

    @Override
    public synchronized void testPlanExecutionFinished(TestPlan testPlan) {
        if (containerWatcher != null) {
            containerWatcher.interrupt();
            containerWatcher = null;
        }
        write("done", null);
        if (out != null) {
            try {
                out.close();
            } catch (IOException ignored) {
                // nothing left to write
            }
            out = null;
        }
    }

    /** A class, a test, or a test template such as a parameterised test; null for anything else (an engine). */
    private static String kind(TestIdentifier identifier) {
        TestSource source = identifier.getSource().orElse(null);
        if (identifier.isTest()) {
            return "test";
        }
        if (source instanceof ClassSource) {
            return "class";
        }
        if (source instanceof MethodSource) {
            return "template";
        }
        return null;
    }

    static String entry(TestIdentifier identifier) {
        String kind = kind(identifier);
        if (kind == null) {
            return null;
        }
        TestSource source = identifier.getSource().orElse(null);
        String className = null;
        String methodName = null;
        if (source instanceof ClassSource) {
            className = ((ClassSource) source).getClassName();
        } else if (source instanceof MethodSource) {
            className = ((MethodSource) source).getClassName();
            methodName = ((MethodSource) source).getMethodName();
        }
        Optional<String> parent = identifier.getParentId();
        return "{\"id\":" + json(identifier.getUniqueId())
            + ",\"parent\":" + json(parent.orElse(null))
            + ",\"kind\":" + json(kind)
            + ",\"className\":" + json(className)
            + ",\"methodName\":" + json(methodName)
            + ",\"name\":" + json(identifier.getDisplayName()) + "}";
    }

    /** The stack trace, its causes included, cut to its first 80 lines. */
    private static String trace(Throwable throwable) {
        StringWriter out = new StringWriter();
        throwable.printStackTrace(new PrintWriter(out));
        String[] lines = out.toString().split("\\r?\\n");
        StringBuilder kept = new StringBuilder();
        for (int i = 0; i < lines.length && i < 80; i++) {
            kept.append(i == 0 ? "" : "\n").append(lines[i]);
        }
        if (lines.length > 80) {
            kept.append("\n\t... ").append(lines.length - 80).append(" more");
        }
        return kept.toString();
    }

    private void write(String event, String fields) {
        if (out == null) {
            return;
        }
        try {
            out.write("{\"event\":" + json(event) + ",\"at\":" + System.currentTimeMillis()
                + (fields == null ? "" : "," + fields) + "}\n");
            out.flush();
        } catch (IOException e) {
            out = null;
        }
    }

    static String json(String value) {
        if (value == null) {
            return "null";
        }
        StringBuilder sb = new StringBuilder(value.length() + 2).append('"');
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            switch (c) {
                case '"': sb.append("\\\""); break;
                case '\\': sb.append("\\\\"); break;
                case '\n': sb.append("\\n"); break;
                case '\r': sb.append("\\r"); break;
                case '\t': sb.append("\\t"); break;
                default:
                    if (c < 0x20) {
                        sb.append(String.format("\\u%04x", (int) c));
                    } else {
                        sb.append(c);
                    }
            }
        }
        return sb.append('"').toString();
    }
}
