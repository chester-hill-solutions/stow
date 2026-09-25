#!/usr/bin/env node
import { runDoctorCli } from "../dist/doctor.js";

process.exitCode = await runDoctorCli(process.argv.slice(2));
