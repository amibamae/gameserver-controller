plugins {
    java
}

group = "io.github.amibamae.gsc"
version = "0.1.0"

repositories {
    mavenCentral()
    maven("https://repo.papermc.io/repository/maven-public/")
}

// Built against the Velocity 3.4 API (Java 17+). Velocity 4.x requires Java
// 25; the API calls this plugin uses have the same signatures in 4.3.1, but
// the plugin has not been built or run against 4.x yet.
val velocityApi = "com.velocitypowered:velocity-api:3.4.0"

dependencies {
    compileOnly(velocityApi)
    annotationProcessor(velocityApi)
    testImplementation(velocityApi)
    testImplementation(platform("org.junit:junit-bom:5.11.4"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

java {
    toolchain {
        languageVersion.set(JavaLanguageVersion.of(21))
    }
}

tasks.withType<JavaCompile>().configureEach {
    options.release.set(17)
    options.encoding = "UTF-8"
    options.compilerArgs.addAll(listOf("-Xlint:all,-processing", "-Werror"))
}

tasks.test {
    useJUnitPlatform()
}
