package testdash;

import java.io.IOException;
import java.io.InputStream;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.io.Writer;
import java.lang.reflect.Field;
import java.net.HttpURLConnection;
import java.net.Proxy;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardCopyOption;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.regex.Pattern;

import org.junit.platform.engine.TestExecutionResult;
import org.junit.platform.engine.TestSource;
import org.junit.platform.engine.UniqueId;
import org.junit.platform.engine.support.descriptor.ClassSource;
import org.junit.platform.engine.support.descriptor.ClasspathResourceSource;
import org.junit.platform.engine.support.descriptor.FilePosition;
import org.junit.platform.engine.support.descriptor.FileSource;
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
 * matches; tests from files, such as Cucumber's scenarios, carry their file and line instead, under groups for
 * the feature, rule and scenario outline. Uses only API present since JUnit Platform 1.0, so one build works with
 * any 1.x launcher.
 *
 * When Testcontainers is on the classpath, containers events name the labels it will clean up by: its session
 * id, and each Compose project's name, registered before the project starts. They tell this run's containers
 * from another run's.
 *
 * As each test starts, and as the run ends, the WireMocks testdash names get their journals copied for it, as
 * tests that reset WireMock take requests out of the journal before testdash would see them (see WireMockJournals).
 */
public class TestdashListener implements TestExecutionListener {

    private static final String RESOURCE_REAPER = "org.testcontainers.utility.ResourceReaper";

    private Writer out;
    private TestPlan plan;
    private Thread containerWatcher;
    private final Set<String> containerLabels = new LinkedHashSet<>();
    private WireMockJournals journals;

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
        plan = testPlan;
        write("plan", "\"entries\":[" + String.join(",", planEntries(testPlan)) + "]");
        watchContainers();
        journals = new WireMockJournals(Paths.get(dir));
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
        String entry = entry(testPlan, identifier);
        if (entry != null) {
            entries.add(entry);
        }
        for (TestIdentifier child : testPlan.getChildren(identifier)) {
            addInRunOrder(testPlan, child, entries);
        }
    }

    @Override
    public synchronized void dynamicTestRegistered(TestIdentifier testIdentifier) {
        String entry = plan == null ? null : entry(plan, testIdentifier);
        if (entry != null) {
            write("registered", "\"entry\":" + entry);
        }
    }

    @Override
    public synchronized void executionStarted(TestIdentifier testIdentifier) {
        if (journals != null && testIdentifier.isTest()) {
            journals.copy();
        }
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
        if (journals != null) {
            journals.copy();
            journals = null;
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

    /**
     * A class, a test, or a test template such as a parameterised test; a group of tests from a file, such as a
     * Cucumber feature, rule or scenario outline, is a class too. Null for anything else, such as an engine.
     */
    private static String kind(TestIdentifier identifier) {
        TestSource source = identifier.getSource().orElse(null);
        if (identifier.isTest()) {
            return "test";
        }
        if (UniqueId.parse(identifier.getUniqueId()).getLastSegment().getType().equals("engine")) {
            return null; // as Cucumber's, which has a source when cucumber.features names the features
        }
        if (source instanceof MethodSource) {
            return "template";
        }
        if (source instanceof ClassSource || source != null && identifier.isContainer()) {
            return "class";
        }
        return null;
    }

    static String entry(TestPlan testPlan, TestIdentifier identifier) {
        String kind = kind(identifier);
        if (kind == null) {
            return null;
        }
        TestSource source = identifier.getSource().orElse(null);
        String className = null;
        String methodName = null;
        String file = null;
        Integer line = null;
        if (source instanceof ClassSource) {
            className = ((ClassSource) source).getClassName();
        } else if (source instanceof MethodSource) {
            className = ((MethodSource) source).getClassName();
            methodName = ((MethodSource) source).getMethodName();
        } else if (source instanceof ClasspathResourceSource) {
            ClasspathResourceSource resource = (ClasspathResourceSource) source;
            file = "classpath:" + resource.getClasspathResourceName();
            line = resource.getPosition().map(FilePosition::getLine).orElse(null);
        } else if (source instanceof FileSource) {
            file = ((FileSource) source).getFile().getPath();
            line = ((FileSource) source).getPosition().map(FilePosition::getLine).orElse(null);
        }
        TestIdentifier parent = parentEntry(testPlan, identifier);
        String name = identifier.getDisplayName();
        if (parent != null && name.startsWith(parent.getDisplayName() + " - ")) {
            name = name.substring(parent.getDisplayName().length() + 3); // as Cucumber's long names repeat their parents'
        }
        String row = "test".equals(kind) && line != null ? tableRow(source, line) : null;
        if (row != null) {
            name = row;
        }
        return "{\"id\":" + json(identifier.getUniqueId())
            + ",\"parent\":" + json(parent == null ? null : parent.getUniqueId())
            + ",\"kind\":" + json(kind)
            + ",\"className\":" + json(className)
            + ",\"methodName\":" + json(methodName)
            + (file == null ? "" : ",\"file\":" + json(file))
            + (line == null ? "" : ",\"line\":" + line)
            + ",\"name\":" + json(name) + "}";
    }

    private static final Map<String, List<String>> fileLines = new HashMap<>();

    /**
     * The cells of the Gherkin table row at a test's line, as "GB | Royal Mail": what tells a Cucumber example from
     * the others of its outline, which it names only by number. Null when the line is not a table row.
     */
    private static String tableRow(TestSource source, int line) {
        List<String> lines = linesOf(source);
        if (lines == null || line < 1 || line > lines.size()) {
            return null;
        }
        String text = lines.get(line - 1).trim();
        if (text.length() < 2 || !text.startsWith("|") || !text.endsWith("|")) {
            return null;
        }
        List<String> cells = new ArrayList<>();
        for (String cell : text.substring(1, text.length() - 1).split("(?<!\\\\)\\|", -1)) {
            cells.add(cell.trim());
        }
        return String.join(" | ", cells);
    }

    /** A test source's file as lines, read once; null when it cannot be read. */
    private static synchronized List<String> linesOf(TestSource source) {
        String key;
        if (source instanceof ClasspathResourceSource) {
            key = "classpath:" + ((ClasspathResourceSource) source).getClasspathResourceName();
        } else if (source instanceof FileSource) {
            key = ((FileSource) source).getFile().getPath();
        } else {
            return null;
        }
        if (fileLines.containsKey(key)) {
            return fileLines.get(key);
        }
        List<String> lines = null;
        try {
            byte[] bytes = null;
            if (source instanceof FileSource) {
                bytes = Files.readAllBytes(((FileSource) source).getFile().toPath());
            } else {
                ClassLoader loader = Thread.currentThread().getContextClassLoader();
                if (loader == null) {
                    loader = TestdashListener.class.getClassLoader();
                }
                try (InputStream in = loader.getResourceAsStream(key.substring("classpath:".length()))) {
                    if (in != null) {
                        bytes = in.readAllBytes();
                    }
                }
            }
            if (bytes != null) {
                lines = Arrays.asList(new String(bytes, StandardCharsets.UTF_8).split("\\r?\\n", -1));
            }
        } catch (IOException | RuntimeException e) {
            lines = null;
        }
        fileLines.put(key, lines);
        return lines;
    }

    /** The nearest ancestor that is an entry too, past what is not, such as the Cucumber engine in a suite. */
    private static TestIdentifier parentEntry(TestPlan testPlan, TestIdentifier identifier) {
        Optional<TestIdentifier> parent = testPlan.getParent(identifier);
        while (parent.isPresent() && kind(parent.get()) == null) {
            parent = testPlan.getParent(parent.get());
        }
        return parent.orElse(null);
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

    /**
     * Copies WireMock journals for testdash. testdash puts a file in [testdash.dir]/wiremock for each WireMock it
     * can reach, named for the service and holding its base URL. A copy is the requests WireMock logged since a
     * little before the last copy, and its near misses when one went unmatched, written whole to
     * [testdash.dir]/wiremock-journal/[pid]-[n]-[service].requests.json (or .near-misses.json). A WireMock that
     * does not answer three times running is left alone until the files change, so it cannot slow the tests.
     */
    static final class WireMockJournals {
        private static final Pattern UNMATCHED = Pattern.compile("\"wasMatched\"\\s*:\\s*false");
        private static final long CLOCK_SLACK_MILLIS = 2000;

        private final Path targets;
        private final Path copies;
        private final String prefix = ProcessHandle.current().pid() + "-";
        private final Map<String, String> baseUrls = new LinkedHashMap<>();
        private final Map<String, Long> copiedAt = new HashMap<>();
        private final Map<String, Integer> failures = new HashMap<>();
        private long listed = Long.MIN_VALUE;
        private int copied;

        WireMockJournals(Path dir) {
            targets = dir.resolve("wiremock");
            copies = dir.resolve("wiremock-journal");
        }

        void copy() {
            try {
                if (!Files.isDirectory(targets)) {
                    return;
                }
                long modified = Files.getLastModifiedTime(targets).toMillis();
                if (modified != listed) {
                    listed = modified;
                    list();
                }
                for (Map.Entry<String, String> target : baseUrls.entrySet()) {
                    String service = target.getKey();
                    if (failures.getOrDefault(service, 0) >= 3) {
                        continue;
                    }
                    long now = System.currentTimeMillis();
                    Long since = copiedAt.get(service);
                    String query = since == null
                        ? "?limit=1000"
                        : "?since=" + Instant.ofEpochMilli(since - CLOCK_SLACK_MILLIS);
                    String journal = get(target.getValue() + "/__admin/requests" + query);
                    if (journal == null) {
                        failures.merge(service, 1, Integer::sum);
                        continue;
                    }
                    failures.remove(service);
                    copiedAt.put(service, now);
                    save(service, "requests", journal);
                    if (UNMATCHED.matcher(journal).find()) {
                        String nearMisses = get(target.getValue() + "/__admin/requests/unmatched/near-misses");
                        if (nearMisses != null) {
                            save(service, "near-misses", nearMisses);
                        }
                    }
                }
            } catch (IOException | RuntimeException ignored) {
                // a copy is a nicety; the run goes on without it
            }
        }

        private void list() throws IOException {
            baseUrls.clear();
            failures.clear();
            try (DirectoryStream<Path> files = Files.newDirectoryStream(targets)) {
                for (Path file : files) {
                    String url = new String(Files.readAllBytes(file), StandardCharsets.UTF_8).trim();
                    if (url.startsWith("http")) {
                        baseUrls.put(file.getFileName().toString(), url);
                    }
                }
            }
        }

        private void save(String service, String kind, String body) throws IOException {
            Files.createDirectories(copies);
            String name = prefix + (copied++) + "-" + service + "." + kind + ".json";
            Path partial = copies.resolve(name + ".partial");
            Files.write(partial, body.getBytes(StandardCharsets.UTF_8));
            Files.move(partial, copies.resolve(name), StandardCopyOption.ATOMIC_MOVE);
        }

        private static String get(String url) {
            HttpURLConnection connection = null;
            try {
                connection = (HttpURLConnection) URI.create(url).toURL().openConnection(Proxy.NO_PROXY);
                connection.setConnectTimeout(250);
                connection.setReadTimeout(2000);
                if (connection.getResponseCode() != 200) {
                    return null;
                }
                try (InputStream body = connection.getInputStream()) {
                    return new String(body.readAllBytes(), StandardCharsets.UTF_8);
                }
            } catch (IOException | RuntimeException e) {
                return null;
            } finally {
                if (connection != null) {
                    connection.disconnect();
                }
            }
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
