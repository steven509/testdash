"""Hooks testdash into Gradle without changing the build: a JUnit Platform listener for runs, and JUnit
discovery for listing a build's tests.

The Java side (junit/*.java) is compiled once, against JUnit Platform jars from the local Gradle cache, and
cached under ~/.cache/testdash. Gradle gets it through the init script in junit/.
"""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
JAVA_SOURCES = sorted((HERE / "junit").glob("*.java"))
INIT_SCRIPT = HERE / "junit" / "testdash.init.gradle"
LISTENER_CLASS = "testdash.TestdashListener"
GRADLE_CACHE = Path.home() / ".gradle" / "caches" / "modules-2" / "files-2.1"
API_MODULES = ("junit-platform-launcher", "junit-platform-engine", "junit-platform-commons")
# Bump when the way the jar is built changes, so cached jars are rebuilt.
BUILD_VERSION = b"1"


class ListenerUnavailable(Exception):
    pass


def is_gradle(command: list[str]) -> bool:
    return bool(command) and os.path.basename(command[0]) in ("gradlew", "gradle", "gradlew.bat")


def discover_command(gradlew: Path, output_dir: Path) -> list[str]:
    """Lists every JUnit Platform Test task's tests into output_dir, one <task>.json each, running none."""
    return [str(gradlew), "-I", str(INIT_SCRIPT), f"-Ptestdash.discover={output_dir}",
            f"-Ptestdash.listenerJar={listener_jar()}", "testdashDiscover", "--continue", "--quiet"]


def with_listener(command: list[str], run_dir: Path, quiet_test_output: bool) -> list[str]:
    """The Gradle command with the init script and its properties added after the executable."""
    extra = [
        "-I", str(INIT_SCRIPT),
        f"-Ptestdash.dir={run_dir}",
        f"-Ptestdash.listenerJar={listener_jar()}",
    ]
    if quiet_test_output:
        extra.append("-Ptestdash.quietTestOutput=true")
    return command[:1] + extra + command[1:]


def listener_jar() -> Path:
    source = b"".join(path.read_bytes() for path in JAVA_SOURCES) + BUILD_VERSION
    cache = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache")) / "testdash"
    jar = cache / f"listener-{hashlib.sha1(source).hexdigest()[:12]}.jar"
    if not jar.exists():
        cache.mkdir(parents=True, exist_ok=True)
        _build(jar)
    return jar


def _build(jar: Path) -> None:
    classpath = _junit_api_jars()
    javac = _javac()
    with tempfile.TemporaryDirectory() as tmp:
        classes = Path(tmp)
        result = subprocess.run(
            [javac, "--release", "11", "-nowarn", "-implicit:none", "-d", str(classes), "-cp", os.pathsep.join(map(str, classpath)),
             *map(str, JAVA_SOURCES)],
            capture_output=True, text=True,
        )
        if result.returncode != 0:
            raise ListenerUnavailable(f"javac failed:\n{result.stderr.strip()}")
        partial = jar.with_suffix(".tmp")
        with zipfile.ZipFile(partial, "w") as zf:
            for path in classes.rglob("*.class"):
                zf.write(path, path.relative_to(classes).as_posix())
            zf.writestr("META-INF/services/org.junit.platform.launcher.TestExecutionListener", LISTENER_CLASS + "\n")
        partial.replace(jar)


def _javac() -> str:
    java_home = os.environ.get("JAVA_HOME")
    if java_home and (Path(java_home) / "bin" / "javac").exists():
        return str(Path(java_home) / "bin" / "javac")
    javac = shutil.which("javac")
    if not javac:
        raise ListenerUnavailable("no javac found (set JAVA_HOME or put a JDK on PATH)")
    return javac


def _junit_api_jars() -> list[Path]:
    """Launcher, engine and commons jars of one JUnit Platform version present in the Gradle cache."""
    versions = None
    for module in API_MODULES:
        found = {p.name for p in (GRADLE_CACHE / "org.junit.platform" / module).glob("*") if p.is_dir()}
        versions = found if versions is None else versions & found
    if not versions:
        raise ListenerUnavailable(
            f"no JUnit Platform jars in {GRADLE_CACHE}; run any JUnit 5 build with Gradle once first")
    version = max(versions, key=_version_key)
    jars = []
    for module in API_MODULES:
        candidates = [p for p in (GRADLE_CACHE / "org.junit.platform" / module / version).glob(f"*/{module}-{version}.jar")]
        if not candidates:
            raise ListenerUnavailable(f"{module} {version} has no jar in the Gradle cache")
        jars.append(candidates[0])
    jars += [p for p in (GRADLE_CACHE / "org.apiguardian" / "apiguardian-api").glob("*/*/apiguardian-api-*.jar")
             if not p.name.endswith(("-sources.jar", "-javadoc.jar"))][:1]
    return jars


def _version_key(version: str) -> tuple:
    return tuple(int(part) if part.isdigit() else -1 for part in version.replace("-", ".").split("."))
