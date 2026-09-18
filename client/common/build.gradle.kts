import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// The UI and networking shared by the Android and desktop apps.
plugins {
    alias(libs.plugins.kotlinMultiplatform)
    alias(libs.plugins.androidMultiplatformLibrary)
    alias(libs.plugins.composeCompiler)
    alias(libs.plugins.composeMultiplatform)
}

kotlin {
    jvm("desktop")

    android {
        namespace = "io.anywherefile.client.common"
        compileSdk { version = release(37) }
        minSdk = 26
        compilerOptions { jvmTarget = JvmTarget.JVM_17 }
    }

    sourceSets {
        commonMain.dependencies {
            implementation(compose.runtime)
            implementation(compose.foundation)
            implementation(compose.material3)
            implementation(compose.ui)
        }
    }
}
