set fixture [file join [file dirname [info script]] prepare.cfg]

proc run_case {name before after permission route should_fail} {
    set mock [interp create]
    $mock eval {
        rename source real_source
        proc source args {}
        proc find args { return interface }
        foreach command {transport adapter gdb_port tcl_port telnet_port swd dap target init sleep halt echo reg resume shutdown} {
            proc $command args {}
        }
        set selected rp2350.core0
        set reset 0
        set reset_done 0
        set writes {}
        set loaded 0
        proc targets {core} { set ::selected $core }
        proc mdw args { return 0 }
        proc mww {address value} {
            lappend ::writes $address
            if {$address == 0x4001a004} { set ::reset 0x01000000 }
            if {$address == 0x4001b004} { set ::reset 0; set ::reset_done 1 }
        }
        proc load_image args { set ::loaded 1 }
        proc verify_image args { error "Target checksum algorithms must not run" }
        proc read_memory {address width count} {
            if {$address == 0x20040000} { return $::expected }
            if {$address == 0x40018004} { return [list $::reset] }
            if {$address == 0xe000ee08} {
                if {$::selected eq "rp2350.core0"} { return {0x30000} }
                return [list [expr {$::reset_done ? $::after : $::before}]]
            }
            if {$address == 0xe000edf0} { return [list $::permission] }
            if {$address == 0xe0042140} { return {15} }
            if {$address == 0xe0042020 && $::selected eq "rp2350.core1"} { return [list $::route] }
            return {0}
        }
    }
    $mock eval [list set before $before]
    $mock eval [list set after $after]
    $mock eval [list set permission $permission]
    $mock eval [list set route $route]
    set failed [catch {$mock eval [list real_source $::fixture]} result]
    set writes [$mock eval {set writes}]
    interp delete $mock
    if {$failed != $should_fail} {
        error "$name: expected failure=$should_fail, got failure=$failed ($result)"
    }
    if {$should_fail && [lsearch -exact $writes 0xe000ed94] != -1} {
        error "$name: wrote MPU before rejecting the conflicting state"
    }
    puts "$name: passed"
}

run_case current-secure 0x30000 0x30000 0x100000 0 0
run_case selected-secure 0x30003 0x30003 0x100000 0 0
run_case selected-nonsecure 0x30001 0x30001 0x100000 0 1
run_case override-after-reset 0x30000 0x30001 0x100000 0 1
run_case nonsecure-execution 0x20000 0x20000 0x100000 0 1
run_case permission-lost 0x30000 0x30000 0 0 1
run_case inherited-route 0x30000 0x30000 0x100000 1 1
