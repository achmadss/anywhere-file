import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// The UI and networking shared by the Android and desktop apps.
plugins {
    alias(libs.plugins.kotlinMultiplatform)
    alias(libs.plugins.androidMultiplatformLibrary)
    alias(libs.plugins.composeCompiler)
    alias(libs.plugins.composeMultiplatform)
    alias(libs.plugins.kotlinSerialization)
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
            api(compose.runtime)
            api(compose.foundation)
            api(compose.material3)
            api(compose.ui)
            implementation(libs.kotlinx.serialization.json)
        }
        commonTest.dependencies {
            implementation(kotlin("test"))
        }
        // Both targets are JVMs, so what talks HTTP lives once, in src/jvmMain, and is
        // compiled into each.
        androidMain {
            kotlin.srcDirs("src/jvmMain/kotlin")
        }
        val desktopMain by getting {
            kotlin.srcDirs("src/jvmMain/kotlin")
        }
    }
}
