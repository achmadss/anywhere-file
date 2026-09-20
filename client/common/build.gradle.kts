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
            implementation(libs.compose.material.icons.extended)
            api(compose.ui)
            implementation(libs.kotlinx.serialization.json)
        }
        commonTest.dependencies {
            implementation(kotlin("test"))
        }
        // Both targets are JVMs, so what talks HTTP lives once, in src/jvmMain, and is
        // compiled into each. Bouncy Castle is there for one function: the JVM can check an
        // Ed25519 signature on its own, and Android only from API 33, which is four years
        // newer than the oldest phone this runs on (#127).
        androidMain {
            kotlin.srcDirs("src/jvmMain/kotlin")
            dependencies {
                implementation(libs.bouncycastle)
                // For the system back gesture, which the file browser binds so that back
                // goes up a folder before it leaves the screen (#155).
                implementation(libs.androidx.activity.compose)
            }
        }
        val desktopMain by getting {
            kotlin.srcDirs("src/jvmMain/kotlin")
            dependencies { implementation(libs.bouncycastle) }
        }
    }
}
