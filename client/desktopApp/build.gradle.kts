plugins {
    alias(libs.plugins.kotlinJvm)
    alias(libs.plugins.composeCompiler)
    alias(libs.plugins.composeMultiplatform)
}

dependencies {
    implementation(project(":common"))
    implementation(compose.desktop.currentOs)
}

compose.desktop {
    application {
        mainClass = "io.anywherefile.client.MainKt"
    }
}
