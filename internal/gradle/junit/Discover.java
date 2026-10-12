package testdash;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

import org.junit.platform.engine.discovery.DiscoverySelectors;
import org.junit.platform.launcher.EngineFilter;
import org.junit.platform.launcher.TagFilter;
import org.junit.platform.launcher.TestPlan;
import org.junit.platform.launcher.core.LauncherDiscoveryRequestBuilder;
import org.junit.platform.launcher.core.LauncherFactory;

/**
 * Lists a test task's tests with the JUnit Platform discovery API, without running any of them, and writes
 * {"task": ..., "entries": [...]} to a file, entries as TestdashListener's plan event has them. Discovery loads
 * test classes without initialising them, so nothing a test sets up runs.
 *
 * Usage: Discover TASK_PATH OUTPUT_FILE [--include-tag T] [--exclude-tag T] [--include-engine E]
 *        [--exclude-engine E] CLASSES_DIR...
 */
public final class Discover {

    private Discover() {
    }

    public static void main(String[] args) throws IOException {
        String task = args[0];
        Path output = Paths.get(args[1]);
        Set<Path> roots = new LinkedHashSet<>();
        List<String> includeTags = new ArrayList<>();
        List<String> excludeTags = new ArrayList<>();
        List<String> includeEngines = new ArrayList<>();
        List<String> excludeEngines = new ArrayList<>();
        for (int i = 2; i < args.length; i++) {
            switch (args[i]) {
                case "--include-tag": includeTags.add(args[++i]); break;
                case "--exclude-tag": excludeTags.add(args[++i]); break;
                case "--include-engine": includeEngines.add(args[++i]); break;
                case "--exclude-engine": excludeEngines.add(args[++i]); break;
                default:
                    if (Files.isDirectory(Paths.get(args[i]))) {
                        roots.add(Paths.get(args[i]));
                    }
            }
        }

        List<String> entries = new ArrayList<>();
        if (!roots.isEmpty()) {
            LauncherDiscoveryRequestBuilder request = LauncherDiscoveryRequestBuilder.request()
                .selectors(DiscoverySelectors.selectClasspathRoots(roots));
            if (!includeTags.isEmpty()) {
                request.filters(TagFilter.includeTags(includeTags));
            }
            if (!excludeTags.isEmpty()) {
                request.filters(TagFilter.excludeTags(excludeTags));
            }
            if (!includeEngines.isEmpty()) {
                request.filters(EngineFilter.includeEngines(includeEngines));
            }
            if (!excludeEngines.isEmpty()) {
                request.filters(EngineFilter.excludeEngines(excludeEngines));
            }
            TestPlan plan = LauncherFactory.create().discover(request.build());
            entries = TestdashListener.planEntries(plan);
        }

        Files.createDirectories(output.toAbsolutePath().getParent());
        Files.write(output, ("{\"task\":" + TestdashListener.json(task) + ",\"entries\":["
            + String.join(",", entries) + "]}\n").getBytes(StandardCharsets.UTF_8));
    }
}
