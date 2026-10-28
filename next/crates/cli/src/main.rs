#![forbid(unsafe_code)]
fn main() -> std::process::ExitCode {
    std::process::ExitCode::from(cli::main_entry())
}
