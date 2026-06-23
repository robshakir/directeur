use tauri_plugin_shell::ShellExt;

#[tauri::command]
fn start_local_engine(app: tauri::AppHandle) -> Result<(), String> {
    let sidecar_command = app.shell().sidecar("directeur").map_err(|e| e.to_string())?.args(["-serve"]);
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
