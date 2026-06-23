use tauri::Manager;
use tauri_plugin_shell::ShellExt;

#[tauri::command]
fn start_local_engine(app: tauri::AppHandle) -> Result<(), String> {
    let home = app.path().home_dir().map_err(|e| e.to_string())?;
    let app_data = home.join(".directeur");
    let app_cache = app.path().app_cache_dir().map_err(|e| e.to_string())?;
    
    std::fs::create_dir_all(&app_data).map_err(|e| e.to_string())?;
    std::fs::create_dir_all(&app_cache).map_err(|e| e.to_string())?;
    
    let resource_dir = app.path().resource_dir().map_err(|e| e.to_string())?;
    
    // Copy config.json from dev workspace or resources if not exists in app_data
    let config_path = app_data.join("config.json");
    if !config_path.exists() {
        let dev_config = std::path::Path::new("../config.json");
        if dev_config.exists() {
            std::fs::copy(dev_config, &config_path).map_err(|e| e.to_string())?;
        } else {
            let resource_config = resource_dir.join("config.json");
            if resource_config.exists() {
                std::fs::copy(&resource_config, &config_path).map_err(|e| e.to_string())?;
            }
        }
    }

    // Copy schema.json from dev workspace or resources if not exists in app_data
    let schema_path = app_data.join("schema.json");
    if !schema_path.exists() {
        let dev_schema = std::path::Path::new("../schema.json");
        if dev_schema.exists() {
            std::fs::copy(dev_schema, &schema_path).map_err(|e| e.to_string())?;
        } else {
            let resource_schema = resource_dir.join("schema.json");
            if resource_schema.exists() {
                std::fs::copy(&resource_schema, &schema_path).map_err(|e| e.to_string())?;
            }
        }
    }

    let html_path = app_cache.join("ride_dashboard.html");
    let json_path = app_cache.join("ride_analysis.json");

    let sidecar_command = app.shell().sidecar("directeur")
        .map_err(|e| e.to_string())?
        .args([
            "-serve",
            "-output-html", html_path.to_str().ok_or("Invalid html path")?,
            "-output-json", json_path.to_str().ok_or("Invalid json path")?,
            "-config", config_path.to_str().ok_or("Invalid config path")?,
        ])
        .env("DIRECTEUR_DATA_DIR", app_data.to_str().ok_or("Invalid data dir")?);

    sidecar_command.spawn().map_err(|e| e.to_string())?;
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .invoke_handler(tauri::generate_handler![start_local_engine])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
